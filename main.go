package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

type operation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

var scheme = runtime.NewScheme()
var codecs = serializer.NewCodecFactory(scheme)

var nodeTaint = "k8s.smp.io/guaranteed"
var nodeLabel = "k8s.smp.io/guaranteed"
var requiredResourcesList []string
var hardIsolation = false

func init() {
	corev1.AddToScheme(scheme)
	admissionregistrationv1beta1.AddToScheme(scheme)
}

func main() {
	var CertFile string
	var KeyFile string
	var RequiredResources = "cpu,memory"

	flag.StringVar(&CertFile, "tls-cert-file", CertFile, ""+
		"File containing the default x509 Certificate for HTTPS. (CA cert, if any, concatenated "+
		"after server cert).")
	flag.StringVar(&KeyFile, "tls-key-file", KeyFile, ""+
		"File containing the default x509 private key matching --tls-cert-file.")

	flag.StringVar(&nodeTaint, "node-taint", nodeTaint, ""+
		"Node taint")
	flag.StringVar(&nodeLabel, "node-label", nodeLabel, ""+
		"Node label")
	flag.StringVar(&RequiredResources, "required-resources", RequiredResources, ""+
		"What resources should be set in pod.spec.resources.limits to define pod as guaranteed")
	flag.BoolVar(&hardIsolation, "hard-isolation", hardIsolation, ""+
		"Set nodeSelector for guaranteed pods or preferredDuringSchedulingIgnoredDuringExecution")

	flag.Parse()

	requiredResourcesList = strings.Split(RequiredResources, ",")

	http.HandleFunc("/", mkServe())
	server := &http.Server{
		Addr:      ":443",
		TLSConfig: configTLS(CertFile, KeyFile),
	}
	server.ListenAndServeTLS("", "")

}

func configTLS(CertFile string, KeyFile string) *tls.Config {
	sCert, err := tls.LoadX509KeyPair(CertFile, KeyFile)
	if err != nil {
		log.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{sCert},
	}
}

func mkServe() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			if data, err := ioutil.ReadAll(r.Body); err == nil {
				body = data
			}
		}

		// verify the content type is accurate
		contentType := r.Header.Get("Content-Type")

		var reviewResponse *v1beta1.AdmissionResponse

		if contentType != "application/json" {
			log.Printf("contentType=%s, expect application/json", contentType)
			return
		}

		ar := v1beta1.AdmissionReview{}
		deserializer := codecs.UniversalDeserializer()
		if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
			log.Print(err)
			reviewResponse = toAdmissionResponse(err)
		} else {
			reviewResponse = admit(ar)
		}

		response := v1beta1.AdmissionReview{}
		if reviewResponse != nil {
			response.Response = reviewResponse
			response.Response.UID = ar.Request.UID
		}
		// reset the Object and OldObject, they are not needed in a response.
		ar.Request.Object = runtime.RawExtension{}
		ar.Request.OldObject = runtime.RawExtension{}

		resp, err := json.Marshal(response)
		if err != nil {
			log.Print(err)
		}
		if _, err := w.Write(resp); err != nil {
			log.Print(err)
		}
	}
}

func toAdmissionResponse(err error) *v1beta1.AdmissionResponse {
	return &v1beta1.AdmissionResponse{
		Result: &metav1.Status{
			Message: err.Error(),
		},
	}
}

func admit(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		log.Printf("expected resource to be %s", podResource)
		return nil
	}

	if ar.Request.Operation != "CREATE" {
		log.Printf("expected operation to be %s", "CREATE")
		return nil
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		log.Print(err)
		return toAdmissionResponse(err)
	}

	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true

	operations := makePatch(&pod)
	if len(operations) != 0 {
		patch, err := json.Marshal(operations)
		if err != nil {
			log.Print(err)
			return toAdmissionResponse(err)
		}

		reviewResponse.Patch = patch
		pt := v1beta1.PatchTypeJSONPatch
		reviewResponse.PatchType = &pt
	}

	return &reviewResponse
}

func makePatch(pod *corev1.Pod) []*operation {
	ops := []*operation{}

	for _, container := range pod.Spec.Containers {
		if container.Resources.Limits == nil {
			return ops
		}

		for _, resource := range requiredResourcesList {
			_, isSet := container.Resources.Limits[v1.ResourceName(resource)]
			if !isSet {
				return ops
			}
		}
	}

	if !hasToleration(pod) {
		ops = append(ops, makeTolerationOperation(pod))
	}

	if hardIsolation {
		ops = append(ops, makeNodeSelectorOperation(pod))
	} else {
		ops = append(ops, makeNodeAffinityOperation(pod))
	}

	return ops
}

func hasToleration(pod *corev1.Pod) bool {
	for _, toleration := range pod.Spec.Tolerations {
		if toleration.Effect == "" && toleration.Key == nodeTaint {
			return true
		}
	}

	return false
}

func makeTolerationOperation(pod *corev1.Pod) *operation {
	position := len(pod.Spec.Tolerations)

	return &operation{
		Op:   "add",
		Path: fmt.Sprint("/spec/tolerations/", position),
		Value: &corev1.Toleration{
			Key:      nodeTaint,
			Operator: "Exists",
			Value:    "true",
		},
	}
}

func makeNodeSelectorOperation(pod *corev1.Pod) *operation {
	if len(pod.Spec.NodeSelector) == 0 {
		return &operation{
			Op:    "add",
			Path:  "/spec/nodeSelector",
			Value: map[string]string{nodeLabel: "true"},
		}
	} else {
		return &operation{
			Op:    "add",
			Path:  "/spec/nodeSelector/" + jsonPatchEscape(nodeLabel),
			Value: "true",
		}
	}
}

func jsonPatchEscape(str string) string {
	return strings.Replace(strings.Replace(str, "~", "~0", -1), "/", "~1", -1)
}

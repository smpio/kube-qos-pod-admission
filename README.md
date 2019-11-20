# kube-qos-pod-admission

This admission controller will help you to isolate pods with limits from pods without limits.

A pod is considered as QoS ensured if it meets the requirements described below. If it does, it will be executed on nodes where only such pods are running.

QoS ensured pod should have this `spec.resources` specifications:

* `requests.memory` equal to `limits.memory` and not equal to zero
* `requests.cpu` not zero
* `limits.cpu` not zero


## Installation

See [Kubernetes docs](https://kubernetes.io/docs/admin/extensible-admission-controllers/#admission-webhooks).


## Usage

Taint nodes as `k8s.smp.io/qos=enforced:NoExecute` and label them as `k8s.smp.io/qos=enforced`.

Pods that match QoS ensured criterias will be scheduled on these nodes only.

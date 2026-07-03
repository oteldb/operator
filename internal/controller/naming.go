/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// defaultImage is the oteldb image used when the CR does not pin one.
const defaultImage = "ghcr.io/oteldb/oteldb:v0.31.0"

// Container ports exposed by every oteldb node. Names must be <= 15 chars (k8s port-name limit).
const (
	portOTLPGRPC   = 4317
	portOTLPHTTP   = 4318
	portPromRW     = 19291
	portPromHTTP   = 9090
	portTempoHTTP  = 3200
	portLokiHTTP   = 3100
	portPyroscope  = 4040
	portHealth     = 13133
	portSelfMetric = 8090
	// portPeer is the default; the effective value comes from spec.cluster.peerPort.
	portPeer = 7946
)

const (
	configVolumeName = "config"
	dataVolumeName   = "data"
	configMountPath  = "/etc/otel"
	configFileName   = "config.yml"
)

// resourceNames centralizes the derived names for a cluster's child objects.
type resourceNames struct {
	base string
}

func namesFor(cr *dbv1alpha1.OtelDBCluster) resourceNames { return resourceNames{base: cr.Name} }

func (n resourceNames) statefulSet() string   { return n.base }
func (n resourceNames) configMap() string     { return n.base + "-config" }
func (n resourceNames) peerService() string   { return n.base + "-peers" }
func (n resourceNames) clientService() string { return n.base }

// selectorLabels are the immutable pod-selector labels for a cluster's oteldb pods.
func selectorLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "oteldb",
		"app.kubernetes.io/instance": cr.Name,
	}
}

// commonLabels are selectorLabels plus non-selector metadata labels.
func commonLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	l := selectorLabels(cr)
	l["app.kubernetes.io/managed-by"] = "oteldb-operator"
	l["app.kubernetes.io/component"] = "storage"
	return l
}

// mergeLabels returns a new map combining base with extra (extra wins).
func mergeLabels(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

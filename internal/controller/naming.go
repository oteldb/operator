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
	"maps"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// defaultImage is the oteldb image used when the CR does not pin one.
const defaultImage = "ghcr.io/oteldb/oteldb:v0.46.0"

// Repeated string values, extracted so a single source of truth drives config and labels.
const (
	appName        = "oteldb"          // app label value and container name
	ingestAppName  = "odbingest"       // ingest app label value, container name and binary
	queryAppName   = "odbselect"       // query app label value, container name and binary
	valStorage     = "storage"         // oteldb signal-backend value and component label
	valIngest      = "ingest"          // ingest component label
	valQuery       = "query"           // query component label
	defaultDataDir = "/var/lib/oteldb" // default storage.dir / WAL dir
	keyBind        = "bind"            // oteldb per-API bind config key
	// bindDisabled is odbselect's "do not serve this API" bind. An omitted block is not enough:
	// odbselect applies its defaults to every API block unconditionally, so a missing one is served
	// on its default port (see cmd/odbselect/config.go, setDefaults and enabled).
	bindDisabled = "-"

	envAWSAccessKeyID     = "AWS_ACCESS_KEY_ID"
	envAWSSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	envPrometheusHost     = "OTEL_EXPORTER_PROMETHEUS_HOST"
	envPrometheusPort     = "OTEL_EXPORTER_PROMETHEUS_PORT"
	envLogLevel           = "OTEL_LOG_LEVEL"

	// annConfigHash carries the rendered config's digest on a pod template, so a config change rolls
	// the workload.
	annConfigHash = "oteldb.io/config-hash"

	// hintRingFromSpec is the remediation for a reserved ring path: every role renders it from the
	// same two spec fields, which is what keeps them from resolving different owner sets.
	hintRingFromSpec = "use spec.cluster and spec.etcd.endpoints"

	// bindAllHost is the address every listener binds, leaving reachability to the Service.
	bindAllHost = "0.0.0.0"
)

// oteldb config keys shared by the renderer and the reserved-path guard.
const (
	keyMetricsBackend  = "metrics_backend"
	keyTracesBackend   = "traces_backend"
	keyLogsBackend     = "logs_backend"
	keyProfilesBackend = "profiles_backend"

	keyBackend = "backend"
	keyCluster = "cluster"
	keyDir     = "dir"
	keyEtcd    = "etcd"
	keyPort    = "port"
)

// Query API config block keys, shared by cmd/oteldb and cmd/odbselect.
const (
	keyPrometheus = "prometheus"
	keyTempo      = "tempo"
	keyLoki       = "loki"
	keyPyroscope  = "pyroscope"
	// keyHealth is odbselect's health listener block. cmd/oteldb spells the same thing
	// "health_check".
	keyHealth = "health"
)

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

// Port names, shared by the container ports, the Services and the probes.
const (
	portNameOTLPGRPC   = "otlp-grpc"
	portNameOTLPHTTP   = "otlp-http"
	portNamePromRW     = "prom-rw"
	portNamePromHTTP   = "prom-http"
	portNameTempoHTTP  = "tempo-http"
	portNameLokiHTTP   = "loki-http"
	portNamePyroscope  = "pyroscope"
	portNameSelfMetric = "metrics"
	portNameHealth     = "health-check"
	portNamePeer       = "peer"
	// portNameIngestHTTP is odbingest's single HTTP listener: OTLP/HTTP, Prometheus remote write
	// and the health endpoints all share it (see cmd/odbingest/app.go, where otlp.Register and the
	// remote write handler are mounted on one mux bound to prometheus_remote_write.bind).
	portNameIngestHTTP = "ingest-http"
)

const (
	configVolumeName     = "config"
	dataVolumeName       = "data"
	configMountPath      = "/etc/otel"
	configFileName       = "config.yml"
	ingestConfigFileName = "odbingest.yml"
	queryConfigFileName  = "odbselect.yml"
	// ingestBinPath is the odbingest entrypoint inside the oteldb image, which defaults to the
	// oteldb binary.
	ingestBinPath = "/usr/local/bin/odbingest"
	// queryBinPath is the odbselect entrypoint inside the oteldb image.
	queryBinPath = "/usr/local/bin/odbselect"
)

// resourceNames centralizes the derived names for a cluster's child objects.
type resourceNames struct {
	base string
}

func namesFor(cr *dbv1alpha1.OtelDBCluster) resourceNames { return resourceNames{base: cr.Name} }

func (n resourceNames) statefulSet() string      { return n.base }
func (n resourceNames) configMap() string        { return n.base + "-config" }
func (n resourceNames) peerService() string      { return n.base + "-peers" }
func (n resourceNames) clientService() string    { return n.base }
func (n resourceNames) ingestDeployment() string { return n.base + "-ingest" }
func (n resourceNames) ingestConfigMap() string  { return n.base + "-ingest-config" }
func (n resourceNames) ingestService() string    { return n.base + "-ingest" }
func (n resourceNames) queryDeployment() string  { return n.base + "-query" }
func (n resourceNames) queryConfigMap() string   { return n.base + "-query-config" }
func (n resourceNames) queryService() string     { return n.base + "-query" }

// roleSelectorLabels are the immutable pod-selector labels for one role's pods. The role's binary
// is the app name, which is what keeps the storage and ingest pod sets from selecting each other:
// the storage StatefulSet's selector is immutable after creation, so it must keep exactly the
// labels it has always had.
func roleSelectorLabels(cr *dbv1alpha1.OtelDBCluster, app string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     app,
		"app.kubernetes.io/instance": cr.Name,
	}
}

// roleCommonLabels are roleSelectorLabels plus non-selector metadata labels.
func roleCommonLabels(cr *dbv1alpha1.OtelDBCluster, app, component string) map[string]string {
	l := roleSelectorLabels(cr, app)
	l["app.kubernetes.io/managed-by"] = "oteldb-operator"
	l["app.kubernetes.io/component"] = component
	return l
}

// selectorLabels are the pod-selector labels for a cluster's oteldb storage pods.
func selectorLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return roleSelectorLabels(cr, appName)
}

// commonLabels are selectorLabels plus non-selector metadata labels.
func commonLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return roleCommonLabels(cr, appName, valStorage)
}

func ingestSelectorLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return roleSelectorLabels(cr, ingestAppName)
}

func ingestCommonLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return roleCommonLabels(cr, ingestAppName, valIngest)
}

func querySelectorLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return roleSelectorLabels(cr, queryAppName)
}

func queryCommonLabels(cr *dbv1alpha1.OtelDBCluster) map[string]string {
	return roleCommonLabels(cr, queryAppName, valQuery)
}

// mergeLabels returns a new map combining base with extra (extra wins).
func mergeLabels(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	maps.Copy(out, base)
	maps.Copy(out, extra)
	return out
}

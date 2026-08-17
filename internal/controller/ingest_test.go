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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// ingestCluster is testCluster with an ingest pool and a non-default ring shape, so that a renderer
// dropping a ring parameter is visible.
func ingestCluster() *dbv1alpha1.OtelDBCluster {
	cr := testCluster()
	cr.Spec.Cluster.ReplicationFactor = ptr.To[int32](2)
	cr.Spec.Cluster.ShardsPerTenant = ptr.To[int32](8)
	cr.Spec.Cluster.EtcdPrefix = "/tenants/a"
	cr.Spec.Ingest = &dbv1alpha1.IngestSpec{Replicas: ptr.To[int32](4)}
	return cr
}

func renderedIngest(t *testing.T, cr *dbv1alpha1.OtelDBCluster) map[string]any {
	t.Helper()

	out, err := renderIngestConfig(cr, cr.Spec.Etcd.Endpoints)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(out), &cfg))
	return cfg
}

func TestIngestDisabledByDefault(t *testing.T) {
	cr := testCluster()
	require.False(t, ingestEnabled(cr), "an unchanged cluster must stay symmetric")
	require.Zero(t, ingestReplicasOf(cr))
}

func TestRenderIngestConfig(t *testing.T) {
	cfg := renderedIngest(t, ingestCluster())

	cluster, ok := cfg["cluster"].(map[string]any)
	require.True(t, ok, "cluster block missing")
	require.Equal(t, []any{"http://etcd:2379"}, cluster["etcd"])
	require.EqualValues(t, 2, cluster["rf"])
	require.EqualValues(t, 8, cluster["shards_per_tenant"])
	require.Equal(t, "/tenants/a", cluster["root"])
	// odbingest is not a ring member, so it has no peer port to advertise.
	require.NotContains(t, cluster, "port")

	rw, ok := cfg["prometheus_remote_write"].(map[string]any)
	require.True(t, ok, "prometheus_remote_write block missing")
	require.Equal(t, "0.0.0.0:19291", rw["bind"])

	otlp, ok := cfg["otlp"].(map[string]any)
	require.True(t, ok, "otlp block missing")
	require.Equal(t, "0.0.0.0:4317", otlp["grpc_bind"])
}

// A ring parameter that differs between the roles does not fail: the pool resolves a different
// owner set than the storage nodes do, and writes land where no read will look for them. Both
// renderers must therefore agree key for key.
func TestIngestRingMatchesStorageRing(t *testing.T) {
	cr := ingestCluster()

	storageOut, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	require.NoError(t, err)
	var storageCfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(storageOut), &storageCfg))

	storageRing := storageCfg["storage"].(map[string]any)["cluster"].(map[string]any)
	ingestRing := renderedIngest(t, cr)["cluster"].(map[string]any)

	for _, key := range []string{"etcd", "rf", "shards_per_tenant", "root"} {
		require.Equal(t, storageRing[key], ingestRing[key], "ring parameter %q differs between roles", key)
	}
}

func TestRenderIngestConfigExtraConfig(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Ingest.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"prometheus_remote_write":{"time_threshold":"1h"}}`),
	}

	rw := renderedIngest(t, cr)["prometheus_remote_write"].(map[string]any)
	require.Equal(t, "1h", rw["time_threshold"])
	require.Equal(t, "0.0.0.0:19291", rw["bind"], "extraConfig must merge into the block, not replace it")
}

func TestRenderIngestConfigReservedCluster(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Ingest.ExtraConfig = &runtime.RawExtension{Raw: []byte(`{"cluster":{"rf":9}}`)}

	_, err := renderIngestConfig(cr, cr.Spec.Etcd.Endpoints)
	require.ErrorContains(t, err, "spec.ingest.extraConfig")

	var invalid validationError
	require.True(t, errors.As(err, &invalid), "reserved paths must fail as a spec validation error")
}

func TestBuildIngestDeployment(t *testing.T) {
	cr := ingestCluster()
	deploy := buildIngestDeployment(cr, "cafebabe")

	require.Equal(t, "obs-ingest", deploy.Name)
	require.EqualValues(t, 4, *deploy.Spec.Replicas)
	require.Equal(t, "cafebabe", deploy.Spec.Template.Annotations["oteldb.io/config-hash"])

	container := deploy.Spec.Template.Spec.Containers[0]
	require.Equal(t, []string{ingestBinPath}, container.Command)
	require.Equal(t, []string{"--config=/etc/otel/odbingest.yml"}, container.Args)
	require.Equal(t, defaultImage, container.Image, "the ingest pool defaults to the cluster image")
	require.Empty(t, container.VolumeMounts[1:], "odbingest holds no data, so it mounts only its config")

	for _, e := range container.Env {
		require.NotContains(t, e.Name, "OTELDB_CLUSTER_",
			"odbingest joins nothing and must carry no ring identity")
	}

	require.Equal(t, "/readyz", container.ReadinessProbe.HTTPGet.Path)
	require.Equal(t, portNameIngestHTTP, container.ReadinessProbe.HTTPGet.Port.StrVal)
}

func TestBuildIngestDeploymentImageOverride(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Ingest.Image = "ghcr.io/oteldb/oteldb:with-odbingest"

	deploy := buildIngestDeployment(cr, "x")
	require.Equal(t, "ghcr.io/oteldb/oteldb:with-odbingest", deploy.Spec.Template.Spec.Containers[0].Image)
}

// The storage StatefulSet's selector is immutable, so the two roles must be told apart by the app
// name alone. Neither selector may match the other role's pods.
func TestIngestAndStorageSelectorsAreDisjoint(t *testing.T) {
	cr := ingestCluster()

	storagePods := labels.Set(mergeLabels(commonLabels(cr), cr.Spec.PodLabels))
	ingestPods := labels.Set(mergeLabels(ingestCommonLabels(cr), cr.Spec.Ingest.PodLabels))

	storageSelector := labels.SelectorFromSet(selectorLabels(cr))
	ingestSelector := labels.SelectorFromSet(ingestSelectorLabels(cr))

	require.True(t, storageSelector.Matches(storagePods))
	require.True(t, ingestSelector.Matches(ingestPods))
	require.False(t, storageSelector.Matches(ingestPods), "the client Service must not front ingest pods")
	require.False(t, ingestSelector.Matches(storagePods), "the ingest Service must not front storage pods")
}

// odbingest serves OTLP/HTTP on the remote write listener, so the Service publishes the port every
// OTLP/HTTP exporter defaults to and remaps it onto that listener.
func TestBuildIngestServicePorts(t *testing.T) {
	svc := buildIngestService(ingestCluster())

	require.Equal(t, "obs-ingest", svc.Name)
	require.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)

	byName := map[string]corev1.ServicePort{}
	for _, p := range svc.Spec.Ports {
		byName[p.Name] = p
	}

	require.EqualValues(t, portOTLPHTTP, byName[portNameOTLPHTTP].Port)
	require.Equal(t, portNameIngestHTTP, byName[portNameOTLPHTTP].TargetPort.StrVal)
	require.EqualValues(t, portPromRW, byName[portNamePromRW].Port)
	require.Equal(t, portNameIngestHTTP, byName[portNamePromRW].TargetPort.StrVal)
	require.EqualValues(t, portOTLPGRPC, byName[portNameOTLPGRPC].Port)
}

func TestBuildIngestServiceDropsRemoteWriteWithoutMetrics(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Signals.Metrics = ptr.To(false)

	svc := buildIngestService(cr)
	for _, p := range svc.Spec.Ports {
		require.NotEqual(t, portNamePromRW, p.Name)
	}
}

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

// queryCluster is ingestCluster with a query pool too, so every role renders from the same
// non-default ring shape.
func queryCluster() *dbv1alpha1.OtelDBCluster {
	cr := ingestCluster()
	cr.Spec.Query = &dbv1alpha1.QuerySpec{Replicas: ptr.To[int32](3)}
	return cr
}

func renderedQuery(t *testing.T, cr *dbv1alpha1.OtelDBCluster) map[string]any {
	t.Helper()

	out, err := renderQueryConfig(cr, cr.Spec.Etcd.Endpoints)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(out), &cfg))
	return cfg
}

func TestQueryDisabledByDefault(t *testing.T) {
	cr := testCluster()
	require.False(t, queryEnabled(cr), "an unchanged cluster must stay symmetric")
	require.Zero(t, queryReplicasOf(cr))
}

func TestRenderQueryConfig(t *testing.T) {
	cfg := renderedQuery(t, queryCluster())

	cluster, ok := cfg["cluster"].(map[string]any)
	require.True(t, ok, "cluster block missing")
	require.Equal(t, []any{"http://etcd:2379"}, cluster["etcd"])
	require.EqualValues(t, 2, cluster["rf"])
	require.EqualValues(t, 8, cluster["shards_per_tenant"])
	require.Equal(t, "/tenants/a", cluster["root"])
	// odbselect is not a ring member, so it has no peer port to advertise.
	require.NotContains(t, cluster, "port")

	// Each query API is its own listener, unlike odbingest's single shared one.
	for key, want := range map[string]string{
		"prometheus": "0.0.0.0:9090",
		"loki":       "0.0.0.0:3100",
		"tempo":      "0.0.0.0:3200",
		"pyroscope":  "0.0.0.0:4040",
		"health":     "0.0.0.0:13133",
	} {
		block, ok := cfg[key].(map[string]any)
		require.True(t, ok, "%s block missing", key)
		require.Equal(t, want, block["bind"])
	}
}

// A disabled signal must be switched off explicitly: odbselect defaults every API block it does not
// find, so an omitted block would be served on its default port.
func TestRenderQueryConfigDisabledSignal(t *testing.T) {
	cr := queryCluster()
	cr.Spec.Signals.Logs = ptr.To(false)

	cfg := renderedQuery(t, cr)
	require.Equal(t, bindDisabled, cfg["loki"].(map[string]any)["bind"])
	require.Equal(t, "0.0.0.0:9090", cfg["prometheus"].(map[string]any)["bind"])
}

func TestRenderQueryConfigRejectsEverySignalDisabled(t *testing.T) {
	cr := queryCluster()
	cr.Spec.Signals = dbv1alpha1.SignalsSpec{
		Metrics: ptr.To(false), Logs: ptr.To(false), Traces: ptr.To(false), Profiles: ptr.To(false),
	}

	_, err := renderQueryConfig(cr, cr.Spec.Etcd.Endpoints)
	require.Error(t, err, "odbselect would serve nothing")
}

// A ring parameter that differs between the roles does not fail: a role resolves a different owner
// set than the storage nodes do, so writes land where no read will look and reads look where the
// data is not. All three renderers must therefore agree key for key.
func TestRoleRingsMatch(t *testing.T) {
	cr := queryCluster()

	storageOut, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	require.NoError(t, err)
	var storageCfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(storageOut), &storageCfg))

	storageRing := storageCfg["storage"].(map[string]any)["cluster"].(map[string]any)
	rings := map[string]map[string]any{
		"ingest": renderedIngest(t, cr)["cluster"].(map[string]any),
		"query":  renderedQuery(t, cr)["cluster"].(map[string]any),
	}

	for role, ring := range rings {
		for _, key := range []string{"etcd", "rf", "shards_per_tenant", "root"} {
			require.Equal(t, storageRing[key], ring[key],
				"ring parameter %q differs between storage and %s", key, role)
		}
	}
}

func TestRenderQueryConfigExtraConfig(t *testing.T) {
	cr := queryCluster()
	cr.Spec.Query.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"prometheus":{"max_samples":1000},"shutdown_timeout":"1m"}`),
	}

	cfg := renderedQuery(t, cr)
	prom := cfg["prometheus"].(map[string]any)
	require.EqualValues(t, 1000, prom["max_samples"])
	require.Equal(t, "0.0.0.0:9090", prom["bind"], "extraConfig must merge into the block, not replace it")
	require.Equal(t, "1m", cfg["shutdown_timeout"])
}

func TestRenderQueryConfigReservedPaths(t *testing.T) {
	for name, raw := range map[string]string{
		"cluster":         `{"cluster":{"rf":9}}`,
		"prometheus bind": `{"prometheus":{"bind":"0.0.0.0:9999"}}`,
		"health bind":     `{"health":{"bind":"0.0.0.0:9999"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cr := queryCluster()
			cr.Spec.Query.ExtraConfig = &runtime.RawExtension{Raw: []byte(raw)}

			_, err := renderQueryConfig(cr, cr.Spec.Etcd.Endpoints)
			require.ErrorContains(t, err, "spec.query.extraConfig")

			var invalid validationError
			require.True(t, errors.As(err, &invalid), "reserved paths must fail as a spec validation error")
		})
	}
}

func TestBuildQueryDeployment(t *testing.T) {
	cr := queryCluster()
	deploy := buildQueryDeployment(cr, "cafebabe")

	require.Equal(t, "obs-query", deploy.Name)
	require.EqualValues(t, 3, *deploy.Spec.Replicas)
	require.Equal(t, "cafebabe", deploy.Spec.Template.Annotations["oteldb.io/config-hash"])

	container := deploy.Spec.Template.Spec.Containers[0]
	require.Equal(t, []string{queryBinPath}, container.Command)
	require.Equal(t, []string{"--config=/etc/otel/odbselect.yml"}, container.Args)
	require.Equal(t, defaultImage, container.Image, "the query pool defaults to the cluster image")
	require.Empty(t, container.VolumeMounts[1:], "odbselect holds no data, so it mounts only its config")

	for _, e := range container.Env {
		require.NotContains(t, e.Name, "OTELDB_CLUSTER_",
			"odbselect joins nothing and must carry no ring identity")
	}

	// The probes target the health listener, not a query API: it is the one listener odbselect
	// always serves.
	require.Equal(t, "/healthz", container.LivenessProbe.HTTPGet.Path)
	require.Equal(t, "/readyz", container.ReadinessProbe.HTTPGet.Path)
	require.Equal(t, portNameHealth, container.ReadinessProbe.HTTPGet.Port.StrVal)

	byName := map[string]int32{}
	for _, p := range container.Ports {
		byName[p.Name] = p.ContainerPort
	}
	require.EqualValues(t, portHealth, byName[portNameHealth])
	require.EqualValues(t, portPromHTTP, byName[portNamePromHTTP])
	require.NotContains(t, byName, portNameOTLPGRPC, "odbselect does not ingest")
}

func TestBuildQueryDeploymentImageOverride(t *testing.T) {
	cr := queryCluster()
	cr.Spec.Query.Image = "ghcr.io/oteldb/oteldb:with-odbselect"

	deploy := buildQueryDeployment(cr, "x")
	require.Equal(t, "ghcr.io/oteldb/oteldb:with-odbselect", deploy.Spec.Template.Spec.Containers[0].Image)
}

// The storage StatefulSet's selector is immutable, so the roles must be told apart by the app name
// alone. No selector may match another role's pods.
func TestQuerySelectorIsDisjointFromOtherRoles(t *testing.T) {
	cr := queryCluster()

	queryPods := labels.Set(mergeLabels(queryCommonLabels(cr), cr.Spec.Query.PodLabels))
	querySelector := labels.SelectorFromSet(querySelectorLabels(cr))

	require.True(t, querySelector.Matches(queryPods))
	require.False(t, querySelector.Matches(labels.Set(commonLabels(cr))),
		"the query Service must not front storage pods")
	require.False(t, querySelector.Matches(labels.Set(ingestCommonLabels(cr))),
		"the query Service must not front ingest pods")
	require.False(t, labels.SelectorFromSet(selectorLabels(cr)).Matches(queryPods),
		"the client Service must not front query pods")
	require.False(t, labels.SelectorFromSet(ingestSelectorLabels(cr)).Matches(queryPods),
		"the ingest Service must not front query pods")
}

// One Service carries every enabled API, each on its own port: odbselect serves four listeners, not
// one shared mux like odbingest.
func TestBuildQueryServicePorts(t *testing.T) {
	svc := buildQueryService(queryCluster())

	require.Equal(t, "obs-query", svc.Name)
	require.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type)

	byName := map[string]corev1.ServicePort{}
	for _, p := range svc.Spec.Ports {
		byName[p.Name] = p
	}

	for name, port := range map[string]int32{
		portNamePromHTTP:   portPromHTTP,
		portNameLokiHTTP:   portLokiHTTP,
		portNameTempoHTTP:  portTempoHTTP,
		portNamePyroscope:  portPyroscope,
		portNameSelfMetric: portSelfMetric,
	} {
		require.EqualValues(t, port, byName[name].Port, "port %s", name)
		require.Equal(t, name, byName[name].TargetPort.StrVal, "port %s", name)
	}
	require.NotContains(t, byName, portNameHealth, "the health listener is probed, not published")
	require.Len(t, svc.Spec.Ports, 5)
}

func TestBuildQueryServiceDropsDisabledSignals(t *testing.T) {
	cr := queryCluster()
	cr.Spec.Signals.Traces = ptr.To(false)

	svc := buildQueryService(cr)
	for _, p := range svc.Spec.Ports {
		require.NotEqual(t, portNameTempoHTTP, p.Name)
	}
}

// The existing client Service keeps fronting the storage nodes: its selector is immutable, and
// repointing an in-use query endpoint at a new workload is a data-path change.
func TestClientServiceUnchangedByQueryPool(t *testing.T) {
	cr := queryCluster()
	without := testCluster()

	require.Equal(t, buildClientService(without).Spec.Selector, buildClientService(cr).Spec.Selector)
}

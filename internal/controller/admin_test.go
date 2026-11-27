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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

func TestAdminDisabledByDefault(t *testing.T) {
	require.False(t, adminEnabled(testCluster()),
		"the admin API must not be published unless spec.admin asks for it")
}

// The admin port must never reach the client Service: that is where PromQL and OTLP traffic lands,
// and the admin API can trigger maintenance and the stream-cost scan.
func TestClientServiceExcludesAdminPort(t *testing.T) {
	for _, svc := range []*corev1.Service{
		buildClientService(testCluster()),
		buildPeerService(testCluster()),
	} {
		for _, p := range svc.Spec.Ports {
			require.NotEqual(t, int32(portAdmin), p.Port, "%s must not publish the admin API", svc.Name)
			require.NotEqual(t, portNameAdmin, p.Name, "%s must not publish the admin API", svc.Name)
		}
	}
}

func TestBuildAdminService(t *testing.T) {
	cr := testCluster()
	cr.Spec.Admin = &dbv1alpha1.AdminSpec{}

	svc := buildAdminService(cr)
	require.Equal(t, "obs-admin", svc.Name)
	require.Equal(t, "monitoring", svc.Namespace)
	require.Equal(t, corev1.ServiceTypeClusterIP, svc.Spec.Type,
		"the admin API is an operator tool, so it must default to a cluster-internal Service")
	require.Equal(t, selectorLabels(cr), svc.Spec.Selector,
		"the admin API is served by the storage pods")
	require.Equal(t, "admin", svc.Labels["app.kubernetes.io/component"],
		"the component label must separate the admin endpoint from client traffic")

	require.Len(t, svc.Spec.Ports, 1)
	port := svc.Spec.Ports[0]
	require.Equal(t, portNameAdmin, port.Name)
	require.EqualValues(t, portAdmin, port.Port)
	require.EqualValues(t, portAdmin, port.TargetPort.IntVal,
		"the admin port is targeted by number: oteldb always serves it, so declaring a container "+
			"port would roll the StatefulSet for no behaviour change")
}

func TestBuildAdminServiceOverrides(t *testing.T) {
	cr := testCluster()
	cr.Spec.Admin = &dbv1alpha1.AdminSpec{
		Service: dbv1alpha1.ServiceSpec{
			Type:        corev1.ServiceTypeNodePort,
			Annotations: map[string]string{"a": "b"},
		},
	}

	svc := buildAdminService(cr)
	require.Equal(t, corev1.ServiceTypeNodePort, svc.Spec.Type)
	require.Equal(t, map[string]string{"a": "b"}, svc.Annotations)
}

// Enabling spec.admin must not touch the pod template: a Service is all that changes.
func TestAdminDoesNotRollPods(t *testing.T) {
	off := testCluster()
	on := testCluster()
	on.Spec.Admin = &dbv1alpha1.AdminSpec{}

	require.Equal(t,
		buildStatefulSet(off, "hash").Spec.Template,
		buildStatefulSet(on, "hash").Spec.Template,
	)

	cfgOff, err := renderConfig(off, off.Spec.Etcd.Endpoints)
	require.NoError(t, err)
	cfgOn, err := renderConfig(on, on.Spec.Etcd.Endpoints)
	require.NoError(t, err)
	require.Equal(t, cfgOff, cfgOn, "spec.admin renders no config: oteldb serves the API either way")
}

// 9464 is published on all three Services, so the exporter that serves it has to be selected:
// go-faster/sdk defaults OTEL_METRICS_EXPORTER to "otlp" and never starts the /metrics server.
func TestSelfMetricsExporterIsPrometheus(t *testing.T) {
	cr := testCluster()
	cr.Spec.Ingest = &dbv1alpha1.IngestSpec{}
	cr.Spec.Query = &dbv1alpha1.QuerySpec{}

	for name, env := range map[string][]corev1.EnvVar{
		"storage":  podEnv(cr),
		"stateles": statelessPodEnv(cr),
	} {
		got := map[string]string{}
		for _, e := range env {
			got[e.Name] = e.Value
		}
		require.Equal(t, "prometheus", got[envMetricsExporter], "%s pods", name)
		require.Equal(t, bindAllHost, got[envPrometheusHost], "%s pods", name)
		require.Equal(t, "9464", got[envPrometheusPort], "%s pods", name)
	}
}

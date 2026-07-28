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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

func testCluster() *dbv1alpha1.OtelDBCluster {
	return &dbv1alpha1.OtelDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "obs", Namespace: "monitoring"},
		Spec: dbv1alpha1.OtelDBClusterSpec{
			Replicas: ptr.To[int32](5),
			Etcd:     dbv1alpha1.EtcdSpec{Endpoints: []string{"http://etcd:2379"}},
		},
	}
}

func TestRenderConfigFileBackendDefaults(t *testing.T) {
	cr := testCluster()
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("unmarshal rendered config: %v\n%s", err, out)
	}

	for _, k := range []string{"metrics_backend", "traces_backend", "logs_backend", "profiles_backend"} {
		if cfg[k] != "storage" {
			t.Errorf("%s = %v, want storage", k, cfg[k])
		}
	}
	storage, ok := cfg["storage"].(map[string]any)
	if !ok {
		t.Fatalf("storage block missing: %T", cfg["storage"])
	}
	if storage["backend"] != "file" {
		t.Errorf("backend = %v, want file", storage["backend"])
	}
	if storage["dir"] != "/var/lib/oteldb" {
		t.Errorf("dir = %v", storage["dir"])
	}
	if _, hasS3 := storage["s3"]; hasS3 {
		t.Errorf("file backend must not render an s3 block")
	}
	cluster, ok := storage["cluster"].(map[string]any)
	if !ok {
		t.Fatalf("cluster block missing")
	}
	etcd, ok := cluster["etcd"].([]any)
	if !ok || len(etcd) != 1 || etcd[0] != "http://etcd:2379" {
		t.Errorf("etcd = %v", cluster["etcd"])
	}
	// Per-pod identity must NOT be baked into the shared config.
	for _, k := range []string{"id", "addr", "zone"} {
		if _, present := cluster[k]; present {
			t.Errorf("cluster.%s must be injected per-pod, not in the ConfigMap", k)
		}
	}
}

func TestRenderConfigProfilesDisabled(t *testing.T) {
	cr := testCluster()
	cr.Spec.Signals.Profiles = ptr.To(false)
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}
	var cfg map[string]any
	_ = yaml.Unmarshal([]byte(out), &cfg)
	if _, present := cfg["profiles_backend"]; present {
		t.Errorf("profiles_backend should be absent when profiles disabled")
	}
	if _, present := cfg["pyroscope"]; present {
		t.Errorf("pyroscope bind should be absent when profiles disabled")
	}
}

func TestRenderConfigS3(t *testing.T) {
	cr := testCluster()
	cr.Spec.Storage.Backend = dbv1alpha1.StorageBackendS3
	cr.Spec.Storage.S3 = &dbv1alpha1.S3Spec{
		Bucket:         "oteldb",
		Endpoint:       "http://minio:9000",
		ForcePathStyle: true,
	}
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}
	var cfg map[string]any
	_ = yaml.Unmarshal([]byte(out), &cfg)
	storage := cfg["storage"].(map[string]any)
	if storage["backend"] != "s3" {
		t.Errorf("backend = %v, want s3", storage["backend"])
	}
	s3 := storage["s3"].(map[string]any)
	if s3["bucket"] != "oteldb" || s3["endpoint"] != "http://minio:9000" || s3["force_path_style"] != true {
		t.Errorf("s3 block = %v", s3)
	}
	if storage["wal_dir"] != "/var/lib/oteldb" {
		t.Errorf("s3 backend should set wal_dir, got %v", storage["wal_dir"])
	}
}

func TestRenderConfigS3RequiresBucket(t *testing.T) {
	cr := testCluster()
	cr.Spec.Storage.Backend = dbv1alpha1.StorageBackendS3
	if _, err := renderConfig(cr, cr.Spec.Etcd.Endpoints); err == nil {
		t.Fatalf("expected error when s3 backend has no bucket")
	}
}

func TestRenderConfigExtraConfigOverrides(t *testing.T) {
	cr := testCluster()
	cr.Spec.ExtraConfig = &runtime.RawExtension{Raw: []byte(`{"prometheus":{"bind":"0.0.0.0:9091"},"ttl":"720h"}`)}
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}
	var cfg map[string]any
	_ = yaml.Unmarshal([]byte(out), &cfg)
	if cfg["ttl"] != "720h" {
		t.Errorf("extraConfig ttl not applied: %v", cfg["ttl"])
	}
	prom := cfg["prometheus"].(map[string]any)
	if prom["bind"] != "0.0.0.0:9091" {
		t.Errorf("extraConfig should override prometheus.bind: %v", prom["bind"])
	}
}

// Regression: a shallow merge of extraConfig used to replace the whole storage block, silently
// downgrading the backend to memory and dropping the node out of the cluster.
func TestRenderConfigExtraConfigPreservesStorageBlock(t *testing.T) {
	cr := testCluster()
	cr.Spec.Cluster.ReplicationFactor = ptr.To[int32](3)
	cr.Spec.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"storage":{"log_query_parallelism":4}}`),
	}
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(out), &cfg))
	storage, ok := cfg["storage"].(map[string]any)
	require.True(t, ok, "storage block missing")

	require.Equal(t, "file", storage["backend"])
	require.Equal(t, "/var/lib/oteldb", storage["dir"])
	cluster, ok := storage["cluster"].(map[string]any)
	require.True(t, ok, "cluster block missing")
	require.Equal(t, []any{"http://etcd:2379"}, cluster["etcd"])
	require.EqualValues(t, 3, cluster["rf"])

	require.EqualValues(t, 4, storage["log_query_parallelism"], "extraConfig key not merged")
}

func TestRenderConfigExtraConfigReservedPath(t *testing.T) {
	cr := testCluster()
	cr.Spec.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"storage":{"cluster":{"etcd":["http://other:2379"]}}}`),
	}
	_, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	require.ErrorContains(t, err, "storage.cluster")

	var invalid validationError
	require.True(t, errors.As(err, &invalid), "reserved paths must fail as a spec validation error")
}

func TestBuildStatefulSetIdentityAndVolumes(t *testing.T) {
	cr := testCluster()
	sts := buildStatefulSet(cr, "deadbeef")

	if got := *sts.Spec.Replicas; got != 5 {
		t.Errorf("replicas = %d, want 5", got)
	}
	if sts.Spec.ServiceName != "obs-peers" {
		t.Errorf("serviceName = %q, want obs-peers", sts.Spec.ServiceName)
	}
	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("want 1 volumeClaimTemplate, got %d", len(sts.Spec.VolumeClaimTemplates))
	}
	if sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().Cmp(resource.MustParse("50Gi")) != 0 {
		t.Errorf("default data size wrong: %v", sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage())
	}

	env := sts.Spec.Template.Spec.Containers[0].Env
	byName := map[string]corev1.EnvVar{}
	for _, e := range env {
		byName[e.Name] = e
	}
	if byName["OTELDB_CLUSTER_ID"].Value != "$(POD_NAME)" {
		t.Errorf("cluster id env = %q", byName["OTELDB_CLUSTER_ID"].Value)
	}
	wantAddr := "$(POD_NAME).obs-peers.monitoring.svc.cluster.local:7946"
	if byName["OTELDB_CLUSTER_ADDR"].Value != wantAddr {
		t.Errorf("cluster addr env = %q, want %q", byName["OTELDB_CLUSTER_ADDR"].Value, wantAddr)
	}
	if pn := byName["POD_NAME"]; pn.ValueFrom == nil || pn.ValueFrom.FieldRef == nil || pn.ValueFrom.FieldRef.FieldPath != "metadata.name" {
		t.Errorf("POD_NAME must come from the downward API")
	}
	// Config hash annotation drives rollout on config change.
	if sts.Spec.Template.Annotations["oteldb.io/config-hash"] != "deadbeef" {
		t.Errorf("config-hash annotation missing")
	}
}

func TestBuildStatefulSetS3Credentials(t *testing.T) {
	cr := testCluster()
	cr.Spec.Storage.Backend = dbv1alpha1.StorageBackendS3
	cr.Spec.Storage.S3 = &dbv1alpha1.S3Spec{
		Bucket:            "b",
		CredentialsSecret: &dbv1alpha1.S3CredentialsSecret{Name: "minio-creds"},
	}
	sts := buildStatefulSet(cr, "x")
	var gotAK, gotSK bool
	for _, e := range sts.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "AWS_ACCESS_KEY_ID" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			gotAK = e.ValueFrom.SecretKeyRef.Name == "minio-creds" && e.ValueFrom.SecretKeyRef.Key == "AWS_ACCESS_KEY_ID"
		}
		if e.Name == "AWS_SECRET_ACCESS_KEY" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			gotSK = e.ValueFrom.SecretKeyRef.Name == "minio-creds"
		}
	}
	if !gotAK || !gotSK {
		t.Errorf("S3 credentials env not wired from secret (ak=%v sk=%v)", gotAK, gotSK)
	}
}

func TestPeerServiceIsHeadless(t *testing.T) {
	svc := buildPeerService(testCluster())
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("peer service must be headless")
	}
	if !svc.Spec.PublishNotReadyAddresses {
		t.Errorf("peer service must publish not-ready addresses so peers resolve before readiness")
	}
}

func TestConfigHashChangesWithContent(t *testing.T) {
	if configHash("a") == configHash("b") {
		t.Errorf("config hash must differ for different content")
	}
	first, second := configHash("a"), configHash("a")
	if first != second {
		t.Errorf("config hash must be stable: %q != %q", first, second)
	}
}

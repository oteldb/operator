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
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

func TestRenderPolicyECAbsentByDefault(t *testing.T) {
	storage := renderStorage(t, testCluster())
	require.NotContains(t, storage, "policy", "an unchanged cluster must render no policy at all")
}

func TestRenderPolicyEC(t *testing.T) {
	cr := testCluster()
	cr.Spec.Policy.EC = &dbv1alpha1.ECSpec{
		Data:   3,
		Parity: 2,
		After:  &metav1.Duration{Duration: 72 * time.Hour},
	}

	policy, ok := renderStorage(t, cr)["policy"].(map[string]any)
	require.True(t, ok, "policy block missing")
	require.Equal(t, map[string]any{
		"data":   float64(3),
		"parity": float64(2),
		"after":  "72h0m0s",
	}, policy["ec"])
}

func TestRenderPolicyECWithoutAfter(t *testing.T) {
	cr := testCluster()
	cr.Spec.Policy.EC = &dbv1alpha1.ECSpec{Data: 4, Parity: 1}

	policy := renderStorage(t, cr)["policy"].(map[string]any)
	require.Equal(t, map[string]any{"data": float64(4), "parity": float64(1)}, policy["ec"],
		"an unset after must be omitted so oteldb reads it as zero: erasure-code every part")
}

// storage.policy.ec joins the rest of the policy block as a reserved extraConfig path.
func TestValidateExtraConfigECReserved(t *testing.T) {
	err := validateExtraConfig(map[string]any{
		"storage": map[string]any{"policy": map[string]any{"ec": map[string]any{"data": 4}}},
	})
	require.ErrorContains(t, err, "storage.policy.ec (use spec.policy.ec)")
}

func TestValidateEC(t *testing.T) {
	ec := func(data, parity int32) *dbv1alpha1.ECSpec {
		return &dbv1alpha1.ECSpec{Data: data, Parity: parity}
	}

	tests := []struct {
		name    string
		mutate  func(*dbv1alpha1.OtelDBCluster)
		wantErr string
	}{
		{
			name:   "absent is valid",
			mutate: func(*dbv1alpha1.OtelDBCluster) {},
		},
		{
			name: "a scheme that fits the cluster",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.EC = ec(3, 2)
			},
		},
		{
			name: "too many shards",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.EC = ec(200, 100)
			},
			wantErr: "at most 256 are allowed",
		},
		{
			name: "negative after",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.EC = ec(3, 2)
				cr.Spec.Policy.EC.After = &metav1.Duration{Duration: -time.Hour}
			},
			wantErr: "spec.policy.ec.after must not be negative",
		},
		{
			name: "a shared s3 backend cannot erasure-code",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Storage.Backend = dbv1alpha1.StorageBackendS3
				cr.Spec.Storage.S3 = &dbv1alpha1.S3Spec{Bucket: "b"}
				cr.Spec.Policy.EC = ec(3, 2)
			},
			wantErr: "needs a private per-node backend",
		},
		{
			name: "an explicitly private s3 backend can",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Storage.Backend = dbv1alpha1.StorageBackendS3
				cr.Spec.Storage.S3 = &dbv1alpha1.S3Spec{Bucket: "b"}
				cr.Spec.Cluster.PrivateBackend = ptr.To(true)
				cr.Spec.Policy.EC = ec(3, 2)
			},
		},
		{
			name: "a file backend declared shared cannot",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Cluster.PrivateBackend = ptr.To(false)
				cr.Spec.Policy.EC = ec(3, 2)
			},
			wantErr: "needs a private per-node backend",
		},
		{
			name: "more shards than nodes",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.EC = ec(4, 2)
			},
			wantErr: "spreads 6 shards (data 4 + parity 2) one per node, but spec.replicas is 5",
		},
		{
			name: "exactly as many shards as nodes",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.EC = ec(4, 1)
			},
		},
		{
			name: "a replication factor alongside is ignored, so it is refused",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Cluster.ReplicationFactor = ptr.To[int32](3)
				cr.Spec.Policy.EC = ec(3, 2)
			},
			wantErr: "spec.policy.ec and spec.cluster.replicationFactor are mutually exclusive",
		},
		{
			name: "a replication factor without ec stays fine",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Cluster.ReplicationFactor = ptr.To[int32](3)
			},
		},
		{
			name: "a tier past the retention window never runs",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.Retention.MaxAge = &metav1.Duration{Duration: 24 * time.Hour}
				cr.Spec.Policy.EC = ec(3, 2)
				cr.Spec.Policy.EC.After = &metav1.Duration{Duration: 48 * time.Hour}
			},
			wantErr: "parts are dropped before they are erasure-coded",
		},
		{
			name: "a tier inside the retention window is fine",
			mutate: func(cr *dbv1alpha1.OtelDBCluster) {
				cr.Spec.Policy.Retention.MaxAge = &metav1.Duration{Duration: 720 * time.Hour}
				cr.Spec.Policy.EC = ec(3, 2)
				cr.Spec.Policy.EC.After = &metav1.Duration{Duration: 48 * time.Hour}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := testCluster()
			tt.mutate(cr)

			err := validatePolicy(cr)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)

			var invalid validationError
			require.True(t, errors.As(err, &invalid), "must be reported as a spec validation error")
		})
	}
}

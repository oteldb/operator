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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// renderStorage renders cr and returns its storage block.
func renderStorage(t *testing.T, cr *dbv1alpha1.OtelDBCluster) map[string]any {
	t.Helper()
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(out), &cfg))
	storage, ok := cfg["storage"].(map[string]any)
	require.True(t, ok, "storage block missing:\n%s", out)
	return storage
}

func TestRenderPolicyAbsentByDefault(t *testing.T) {
	storage := renderStorage(t, testCluster())
	require.NotContains(t, storage, "policy",
		"no policy block should be rendered when neither retention nor limits is set")
}

func TestRenderPolicyRetention(t *testing.T) {
	cr := testCluster()
	cr.Spec.Retention = dbv1alpha1.RetentionSpec{
		MaxAge:   &metav1.Duration{Duration: 720 * time.Hour},
		MaxBytes: ptr.To(resource.MustParse("500Gi")),
	}

	policy, ok := renderStorage(t, cr)["policy"].(map[string]any)
	require.True(t, ok, "policy block missing")
	require.Equal(t, map[string]any{
		"max_age":   "720h0m0s",
		"max_bytes": float64(500 * 1024 * 1024 * 1024),
	}, policy["retention"])
	require.NotContains(t, policy, "limits", "limits must stay absent when unset")
}

func TestRenderPolicyLimits(t *testing.T) {
	cr := testCluster()
	cr.Spec.Limits = dbv1alpha1.LimitsSpec{
		IngestBytesPerSecond: ptr.To(resource.MustParse("50Mi")),
		MaxInFlightBytes:     ptr.To(resource.MustParse("1Gi")),
		MaxSeries:            ptr.To[int64](1_000_000),
		MaxSeriesSoft:        ptr.To[int64](800_000),
		MaxPartSize:          ptr.To(resource.MustParse("256Mi")),
	}

	policy, ok := renderStorage(t, cr)["policy"].(map[string]any)
	require.True(t, ok, "policy block missing")
	require.Equal(t, map[string]any{
		"ingest_bytes_per_second": float64(50 * 1024 * 1024),
		"max_in_flight_bytes":     float64(1024 * 1024 * 1024),
		"max_series":              float64(1_000_000),
		"max_series_soft":         float64(800_000),
		"max_part_size":           float64(256 * 1024 * 1024),
	}, policy["limits"])
	require.NotContains(t, policy, "retention", "retention must stay absent when unset")
}

// The policy must not disturb the rest of the storage block, which is where the shallow-merge bug
// (issue #1) did its damage.
func TestRenderPolicyKeepsStorageBlock(t *testing.T) {
	cr := testCluster()
	cr.Spec.Retention.MaxAge = &metav1.Duration{Duration: time.Hour}

	storage := renderStorage(t, cr)
	require.Equal(t, "file", storage["backend"])
	require.Equal(t, defaultDataDir, storage["dir"])
	require.Contains(t, storage, "cluster")
}

func TestRenderPolicyExtraConfigMergesSiblings(t *testing.T) {
	cr := testCluster()
	cr.Spec.Retention.MaxAge = &metav1.Duration{Duration: 24 * time.Hour}
	cr.Spec.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"storage":{"policy":{"recompress":{"after":"72h","level":19}}}}`),
	}

	policy, ok := renderStorage(t, cr)["policy"].(map[string]any)
	require.True(t, ok, "policy block missing")
	require.Equal(t, map[string]any{"max_age": "24h0m0s"}, policy["retention"],
		"extraConfig must not displace the generated retention")
	require.Equal(t, map[string]any{"after": "72h", "level": float64(19)}, policy["recompress"])
}

func TestValidatePolicy(t *testing.T) {
	tests := []struct {
		name      string
		retention dbv1alpha1.RetentionSpec
		limits    dbv1alpha1.LimitsSpec
		wantErr   string
	}{
		{
			name: "empty is valid",
		},
		{
			name:      "zero max age is retain forever",
			retention: dbv1alpha1.RetentionSpec{MaxAge: &metav1.Duration{}},
		},
		{
			name:      "negative max age",
			retention: dbv1alpha1.RetentionSpec{MaxAge: &metav1.Duration{Duration: -time.Hour}},
			wantErr:   "spec.retention.maxAge must not be negative",
		},
		{
			name:      "negative max bytes",
			retention: dbv1alpha1.RetentionSpec{MaxBytes: ptr.To(resource.MustParse("-1Gi"))},
			wantErr:   "spec.retention.maxBytes must not be negative",
		},
		{
			name:    "negative ingest rate",
			limits:  dbv1alpha1.LimitsSpec{IngestBytesPerSecond: ptr.To(resource.MustParse("-1"))},
			wantErr: "spec.limits.ingestBytesPerSecond must not be negative",
		},
		{
			name:    "negative max part size",
			limits:  dbv1alpha1.LimitsSpec{MaxPartSize: ptr.To(resource.MustParse("-256Mi"))},
			wantErr: "spec.limits.maxPartSize must not be negative",
		},
		{
			name:    "soft budget without hard ceiling",
			limits:  dbv1alpha1.LimitsSpec{MaxSeriesSoft: ptr.To[int64](1000)},
			wantErr: "spec.limits.maxSeriesSoft needs spec.limits.maxSeries",
		},
		{
			name: "soft budget above hard ceiling",
			limits: dbv1alpha1.LimitsSpec{
				MaxSeries:     ptr.To[int64](1000),
				MaxSeriesSoft: ptr.To[int64](2000),
			},
			wantErr: "must not exceed spec.limits.maxSeries",
		},
		{
			name: "soft budget equal to hard ceiling",
			limits: dbv1alpha1.LimitsSpec{
				MaxSeries:     ptr.To[int64](1000),
				MaxSeriesSoft: ptr.To[int64](1000),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := testCluster()
			cr.Spec.Retention = tt.retention
			cr.Spec.Limits = tt.limits

			err := validatePolicy(cr)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)

			// A bad policy is a spec problem, so it must not be requeued.
			var invalid validationError
			require.ErrorAs(t, err, &invalid)

			// renderConfig rejects it too, rather than emitting a config oteldb would refuse.
			_, err = renderConfig(cr, cr.Spec.Etcd.Endpoints)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateExtraConfigReservedPolicyPaths(t *testing.T) {
	tests := []struct {
		name    string
		extra   map[string]any
		wantErr string
	}{
		{
			name: "retention is reserved",
			extra: map[string]any{"storage": map[string]any{
				"policy": map[string]any{"retention": map[string]any{"max_age": "1h"}},
			}},
			wantErr: "storage.policy.retention (use spec.retention)",
		},
		{
			name: "limits is reserved",
			extra: map[string]any{"storage": map[string]any{
				"policy": map[string]any{"limits": map[string]any{"max_series": 10}},
			}},
			wantErr: "storage.policy.limits (use spec.limits)",
		},
		{
			name: "a key below a reserved path is reserved too",
			extra: map[string]any{"storage": map[string]any{
				"policy": map[string]any{"retention": map[string]any{
					"max_bytes": map[string]any{"nested": true},
				}},
			}},
			wantErr: "storage.policy.retention",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExtraConfig(tt.extra)
			require.ErrorContains(t, err, tt.wantErr)

			var invalid validationError
			require.True(t, errors.As(err, &invalid), "must be reported as a spec validation error")
		})
	}
}

// The siblings of the reserved policy keys stay open, so the CRD's coverage of retention/limits
// does not lock users out of the rest of storage.policy.
func TestValidateExtraConfigPolicySiblingsAllowed(t *testing.T) {
	for _, key := range []string{"precision", "downsample", "recompress"} {
		t.Run(key, func(t *testing.T) {
			require.NoError(t, validateExtraConfig(map[string]any{
				"storage": map[string]any{"policy": map[string]any{key: "whatever"}},
			}))
		})
	}
}

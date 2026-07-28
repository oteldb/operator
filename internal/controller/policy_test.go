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
	cr.Spec.Policy.Retention = dbv1alpha1.RetentionSpec{
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
	cr.Spec.Policy.Limits = dbv1alpha1.LimitsSpec{
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
	cr.Spec.Policy.Retention.MaxAge = &metav1.Duration{Duration: time.Hour}

	storage := renderStorage(t, cr)
	require.Equal(t, "file", storage["backend"])
	require.Equal(t, defaultDataDir, storage["dir"])
	require.Contains(t, storage, "cluster")
}

// storage.policy is reserved in full, but its siblings under storage are not: an extraConfig key
// the CRD does not model still merges alongside a generated policy.
func TestRenderPolicyExtraConfigMergesStorageSiblings(t *testing.T) {
	cr := testCluster()
	cr.Spec.Policy.Retention.MaxAge = &metav1.Duration{Duration: 24 * time.Hour}
	cr.Spec.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"storage":{"log_query_parallelism":4}}`),
	}

	storage := renderStorage(t, cr)
	require.EqualValues(t, 4, storage["log_query_parallelism"])

	policy, ok := storage["policy"].(map[string]any)
	require.True(t, ok, "policy block missing")
	require.Equal(t, map[string]any{"max_age": "24h0m0s"}, policy["retention"])
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
			wantErr:   "spec.policy.retention.maxAge must not be negative",
		},
		{
			name:      "negative max bytes",
			retention: dbv1alpha1.RetentionSpec{MaxBytes: ptr.To(resource.MustParse("-1Gi"))},
			wantErr:   "spec.policy.retention.maxBytes must not be negative",
		},
		{
			name:    "negative ingest rate",
			limits:  dbv1alpha1.LimitsSpec{IngestBytesPerSecond: ptr.To(resource.MustParse("-1"))},
			wantErr: "spec.policy.limits.ingestBytesPerSecond must not be negative",
		},
		{
			name:    "negative max part size",
			limits:  dbv1alpha1.LimitsSpec{MaxPartSize: ptr.To(resource.MustParse("-256Mi"))},
			wantErr: "spec.policy.limits.maxPartSize must not be negative",
		},
		{
			name:    "soft budget without hard ceiling",
			limits:  dbv1alpha1.LimitsSpec{MaxSeriesSoft: ptr.To[int64](1000)},
			wantErr: "spec.policy.limits.maxSeriesSoft needs spec.policy.limits.maxSeries",
		},
		{
			name: "soft budget above hard ceiling",
			limits: dbv1alpha1.LimitsSpec{
				MaxSeries:     ptr.To[int64](1000),
				MaxSeriesSoft: ptr.To[int64](2000),
			},
			wantErr: "must not exceed spec.policy.limits.maxSeries",
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
			cr.Spec.Policy.Retention = tt.retention
			cr.Spec.Policy.Limits = tt.limits

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
			wantErr: "storage.policy.retention (use spec.policy.retention)",
		},
		{
			name: "limits is reserved",
			extra: map[string]any{"storage": map[string]any{
				"policy": map[string]any{"limits": map[string]any{"max_series": 10}},
			}},
			wantErr: "storage.policy.limits (use spec.policy.limits)",
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
// Every storage.policy key the CRD models is reserved, so extraConfig cannot fight spec.policy.
func TestValidateExtraConfigPolicyFullyReserved(t *testing.T) {
	for _, key := range []string{"retention", "limits", "downsample", "precision", "recompress"} {
		t.Run(key, func(t *testing.T) {
			err := validateExtraConfig(map[string]any{
				"storage": map[string]any{"policy": map[string]any{key: "whatever"}},
			})
			require.ErrorContains(t, err, "storage.policy."+key+" (use spec.policy."+key+")")
		})
	}
}

func TestRenderPolicyMergeTiers(t *testing.T) {
	cr := testCluster()
	cr.Spec.Policy = dbv1alpha1.PolicySpec{
		Downsample: []dbv1alpha1.DownsampleTierSpec{
			{After: metav1.Duration{Duration: 6 * time.Hour}, Interval: metav1.Duration{Duration: time.Minute}},
			{After: metav1.Duration{Duration: 72 * time.Hour}, Interval: metav1.Duration{Duration: 5 * time.Minute}, Agg: "avg"},
		},
		Precision: []dbv1alpha1.PrecisionTierSpec{
			{After: metav1.Duration{Duration: 72 * time.Hour}, Bits: 32},
		},
		Recompress: &dbv1alpha1.RecompressSpec{
			After: metav1.Duration{Duration: 72 * time.Hour},
			Level: ptr.To[int32](19),
		},
	}

	policy, ok := renderStorage(t, cr)["policy"].(map[string]any)
	require.True(t, ok, "policy block missing")
	require.Equal(t, []any{
		map[string]any{"after": "6h0m0s", "interval": "1m0s"},
		map[string]any{"after": "72h0m0s", "interval": "5m0s", "agg": "avg"},
	}, policy["downsample"], "an unset agg must be omitted so oteldb applies its own default")
	require.Equal(t, []any{
		map[string]any{"after": "72h0m0s", "bits": float64(32)},
	}, policy["precision"])
	require.Equal(t, map[string]any{"after": "72h0m0s", "level": float64(19)}, policy["recompress"])
}

func TestRenderPolicyRecompressWithoutLevel(t *testing.T) {
	cr := testCluster()
	cr.Spec.Policy.Recompress = &dbv1alpha1.RecompressSpec{After: metav1.Duration{Duration: time.Hour}}

	policy := renderStorage(t, cr)["policy"].(map[string]any)
	require.Equal(t, map[string]any{"after": "1h0m0s"}, policy["recompress"],
		"an unset level must be omitted so oteldb picks its best-ratio default")
}

func TestValidateMergeTiers(t *testing.T) {
	hour := func(h int) metav1.Duration { return metav1.Duration{Duration: time.Duration(h) * time.Hour} }
	min := func(m int) metav1.Duration { return metav1.Duration{Duration: time.Duration(m) * time.Minute} }

	tests := []struct {
		name    string
		policy  dbv1alpha1.PolicySpec
		wantErr string
	}{
		{
			name: "empty is valid",
		},
		{
			name: "ordered tiers",
			policy: dbv1alpha1.PolicySpec{
				Downsample: []dbv1alpha1.DownsampleTierSpec{
					{After: hour(6), Interval: min(1)},
					{After: hour(72), Interval: min(5)},
				},
			},
		},
		{
			name: "tier order does not matter",
			policy: dbv1alpha1.PolicySpec{
				Downsample: []dbv1alpha1.DownsampleTierSpec{
					{After: hour(72), Interval: min(5)},
					{After: hour(6), Interval: min(1)},
				},
			},
		},
		{
			name: "zero downsample interval",
			policy: dbv1alpha1.PolicySpec{
				Downsample: []dbv1alpha1.DownsampleTierSpec{{After: hour(6)}},
			},
			wantErr: "spec.policy.downsample[0].interval must be positive",
		},
		{
			name: "negative downsample after",
			policy: dbv1alpha1.PolicySpec{
				Downsample: []dbv1alpha1.DownsampleTierSpec{{After: hour(-1), Interval: min(1)}},
			},
			wantErr: "spec.policy.downsample[0].after must not be negative",
		},
		{
			name: "duplicate downsample after",
			policy: dbv1alpha1.PolicySpec{
				Downsample: []dbv1alpha1.DownsampleTierSpec{
					{After: hour(6), Interval: min(1)},
					{After: hour(6), Interval: min(5)},
				},
			},
			wantErr: "spec.policy.downsample[1].after duplicates spec.policy.downsample[0].after",
		},
		{
			name: "duplicate precision after",
			policy: dbv1alpha1.PolicySpec{
				Precision: []dbv1alpha1.PrecisionTierSpec{
					{After: hour(72), Bits: 32},
					{After: hour(72), Bits: 16},
				},
			},
			wantErr: "spec.policy.precision[1].after duplicates spec.policy.precision[0].after",
		},
		{
			name:    "zero recompress after",
			policy:  dbv1alpha1.PolicySpec{Recompress: &dbv1alpha1.RecompressSpec{}},
			wantErr: "spec.policy.recompress.after must be positive",
		},
		{
			name: "downsample tier past retention",
			policy: dbv1alpha1.PolicySpec{
				Retention:  dbv1alpha1.RetentionSpec{MaxAge: &metav1.Duration{Duration: 24 * time.Hour}},
				Downsample: []dbv1alpha1.DownsampleTierSpec{{After: hour(48), Interval: min(5)}},
			},
			wantErr: "spec.policy.downsample[0].after (48h0m0s) is at or past spec.policy.retention.maxAge",
		},
		{
			name: "precision tier past retention",
			policy: dbv1alpha1.PolicySpec{
				Retention: dbv1alpha1.RetentionSpec{MaxAge: &metav1.Duration{Duration: 24 * time.Hour}},
				Precision: []dbv1alpha1.PrecisionTierSpec{{After: hour(24), Bits: 32}},
			},
			wantErr: "spec.policy.precision[0].after (24h0m0s) is at or past spec.policy.retention.maxAge",
		},
		{
			name: "recompress past retention",
			policy: dbv1alpha1.PolicySpec{
				Retention:  dbv1alpha1.RetentionSpec{MaxAge: &metav1.Duration{Duration: 24 * time.Hour}},
				Recompress: &dbv1alpha1.RecompressSpec{After: hour(48)},
			},
			wantErr: "spec.policy.recompress.after (48h0m0s) is at or past spec.policy.retention.maxAge",
		},
		{
			name: "retain forever does not bound the tiers",
			policy: dbv1alpha1.PolicySpec{
				Retention:  dbv1alpha1.RetentionSpec{MaxAge: &metav1.Duration{}},
				Downsample: []dbv1alpha1.DownsampleTierSpec{{After: hour(8760), Interval: min(60)}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := testCluster()
			cr.Spec.Policy = tt.policy

			err := validatePolicy(cr)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)

			var invalid validationError
			require.ErrorAs(t, err, &invalid, "must be reported as a spec validation error")

			_, err = renderConfig(cr, cr.Spec.Etcd.Endpoints)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

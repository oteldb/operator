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
)

func TestDeepMerge(t *testing.T) {
	tests := []struct {
		name string
		dst  map[string]any
		src  map[string]any
		want map[string]any
	}{
		{
			name: "disjoint keys",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{"b": 2},
			want: map[string]any{"a": 1, "b": 2},
		},
		{
			name: "leaf wins",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{"a": 2},
			want: map[string]any{"a": 2},
		},
		{
			name: "nested maps merge",
			dst:  map[string]any{"storage": map[string]any{"backend": "file", "dir": "/data"}},
			src:  map[string]any{"storage": map[string]any{"policy": "keep"}},
			want: map[string]any{"storage": map[string]any{"backend": "file", "dir": "/data", "policy": "keep"}},
		},
		{
			name: "nested merge recurses",
			dst: map[string]any{"storage": map[string]any{
				"cluster": map[string]any{"rf": 3, "port": 7946},
			}},
			src: map[string]any{"storage": map[string]any{
				"cluster": map[string]any{"rf": 5},
			}},
			want: map[string]any{"storage": map[string]any{
				"cluster": map[string]any{"rf": 5, "port": 7946},
			}},
		},
		{
			name: "map replaces scalar",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{"a": map[string]any{"b": 2}},
			want: map[string]any{"a": map[string]any{"b": 2}},
		},
		{
			name: "scalar replaces map",
			dst:  map[string]any{"a": map[string]any{"b": 2}},
			src:  map[string]any{"a": "x"},
			want: map[string]any{"a": "x"},
		},
		{
			name: "lists replace, never merge",
			dst:  map[string]any{"etcd": []any{"a", "b"}},
			src:  map[string]any{"etcd": []any{"c"}},
			want: map[string]any{"etcd": []any{"c"}},
		},
		{
			name: "explicit null replaces",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{"a": nil},
			want: map[string]any{"a": nil},
		},
		{
			name: "empty source keeps destination",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{},
			want: map[string]any{"a": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deepMerge(tt.dst, tt.src)
			require.Equal(t, tt.want, tt.dst)
		})
	}
}

func TestValidateExtraConfig(t *testing.T) {
	tests := []struct {
		name    string
		extra   map[string]any
		wantErr string // substring; empty means the config must be accepted
	}{
		{
			name:  "unrelated keys",
			extra: map[string]any{"ttl": "720h", "prometheus": map[string]any{"bind": ":9091"}},
		},
		{
			name:  "non-reserved storage key",
			extra: map[string]any{"storage": map[string]any{"log_query_parallelism": 4}},
		},
		{
			name:    "storage backend",
			extra:   map[string]any{"storage": map[string]any{"backend": "memory"}},
			wantErr: "storage.backend (use spec.storage.backend)",
		},
		{
			name:    "storage dir",
			extra:   map[string]any{"storage": map[string]any{"dir": "/tmp"}},
			wantErr: "storage.dir",
		},
		{
			name:    "cluster subtree",
			extra:   map[string]any{"storage": map[string]any{"cluster": map[string]any{"etcd": []any{"http://evil:2379"}}}},
			wantErr: "storage.cluster (use spec.cluster and spec.etcd.endpoints)",
		},
		{
			name:    "s3 subtree",
			extra:   map[string]any{"storage": map[string]any{"s3": map[string]any{"bucket": "other"}}},
			wantErr: "storage.s3",
		},
		{
			name:    "engine tuning",
			extra:   map[string]any{"storage": map[string]any{"flush_interval": "1m"}},
			wantErr: "storage.flush_interval (use spec.engine.flushInterval)",
		},
		{
			name:    "signal backend",
			extra:   map[string]any{"metrics_backend": "clickhouse"},
			wantErr: "metrics_backend",
		},
		{
			name: "several paths are all reported",
			extra: map[string]any{"storage": map[string]any{
				"backend": "memory",
				"dir":     "/tmp",
			}},
			wantErr: "reserved config paths: storage.backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExtraConfig(tt.extra)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErr)
			var invalid validationError
			require.True(t, errors.As(err, &invalid), "must be reported as a spec validation error")
		})
	}
}

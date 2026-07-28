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
	"fmt"
	"slices"
	"strings"
)

const embeddedStorageHint = "the operator always serves signals from the embedded storage engine"

// reservedConfigPaths are the config paths the operator renders and owns: setting them from
// spec.extraConfig would silently fight the spec (and, for storage.backend/cluster, cost durability
// or cluster membership). Each maps to the spec field that must be used instead.
//
// A reserved path also covers everything below it, so "storage.cluster" rejects
// "storage.cluster.etcd" too.
var reservedConfigPaths = map[string]string{
	keyMetricsBackend:  embeddedStorageHint,
	keyTracesBackend:   embeddedStorageHint,
	keyLogsBackend:     embeddedStorageHint,
	keyProfilesBackend: "use spec.signals.profiles",

	"storage.backend":             "use spec.storage.backend",
	"storage.dir":                 "use spec.storage.dir",
	"storage.wal_dir":             "use spec.storage.dir",
	"storage.s3":                  "use spec.storage.s3",
	"storage.cluster":             "use spec.cluster and spec.etcd.endpoints",
	"storage.flush_interval":      "use spec.engine.flushInterval",
	"storage.read_cache_bytes":    "use spec.engine.readCacheSize",
	"storage.decode_cache_bytes":  "use spec.engine.decodeCacheSize",
	"storage.decode_memory_bytes": "use spec.engine.decodeMemoryLimit",
	"storage.aggregate_stats":     "use spec.engine.aggregateStats",

	"storage.policy.retention":  "use spec.policy.retention",
	"storage.policy.limits":     "use spec.policy.limits",
	"storage.policy.downsample": "use spec.policy.downsample",
	"storage.policy.precision":  "use spec.policy.precision",
	"storage.policy.recompress": "use spec.policy.recompress",
}

// validationError marks a spec problem that no amount of retrying can fix: the reconcile is
// reported on the CR status and not requeued until the spec changes.
type validationError struct {
	err error
}

func (e validationError) Error() string { return e.err.Error() }
func (e validationError) Unwrap() error { return e.err }

func invalidSpec(format string, args ...any) error {
	return validationError{err: fmt.Errorf(format, args...)}
}

// validateExtraConfig rejects extraConfig that targets operator-owned paths.
func validateExtraConfig(extra map[string]any) error {
	var found []string
	collectReservedPaths(extra, "", &found)
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)

	msgs := make([]string, 0, len(found))
	for _, p := range found {
		msgs = append(msgs, fmt.Sprintf("%s (%s)", p, reservedConfigPaths[p]))
	}
	return invalidSpec("spec.extraConfig sets reserved config %s: %s",
		plural(len(found), "path", "paths"), strings.Join(msgs, ", "))
}

func collectReservedPaths(m map[string]any, prefix string, found *[]string) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if _, reserved := reservedConfigPaths[path]; reserved {
			*found = append(*found, path)
			continue // Everything below a reserved path is reserved as well.
		}
		if sub, ok := v.(map[string]any); ok {
			collectReservedPaths(sub, path, found)
		}
	}
}

// deepMerge recursively merges src into dst: nested maps are merged key by key, any other value
// (including a nil, a list, or a map replacing a scalar) replaces the one in dst.
func deepMerge(dst, src map[string]any) {
	for k, sv := range src {
		if sm, ok := sv.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = sv
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

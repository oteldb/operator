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
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// renderConfig builds the oteldb config.yml for a cluster. Per-pod identity (cluster.id, addr,
// zone) is intentionally omitted — it is injected per pod via environment variables so the whole
// StatefulSet can share this single ConfigMap.
func renderConfig(cr *dbv1alpha1.OtelDBCluster, etcdEndpoints []string) (string, error) {
	if err := validateSignals(cr); err != nil {
		return "", err
	}

	cfg := map[string]any{
		"health_check": map[string]any{keyBind: "0.0.0.0:13133"},
	}

	// Every enabled signal is served from the embedded clustered storage engine; no ClickHouse.
	// A disabled signal drops both its backend and its API bind, so the API is not served at all.
	if metricsEnabled(cr) {
		cfg[keyMetricsBackend] = valStorage
		cfg["prometheus"] = map[string]any{keyBind: "0.0.0.0:9090"}
	}
	if tracesEnabled(cr) {
		cfg[keyTracesBackend] = valStorage
		cfg["tempo"] = map[string]any{keyBind: "0.0.0.0:3200"}
	}
	if logsEnabled(cr) {
		cfg[keyLogsBackend] = valStorage
		cfg["loki"] = map[string]any{keyBind: "0.0.0.0:3100"}
	}
	if profilesEnabled(cr) {
		cfg[keyProfilesBackend] = valStorage
		cfg["pyroscope"] = map[string]any{keyBind: "0.0.0.0:4040"}
	}

	storage := map[string]any{
		keyBackend: string(backendOf(cr)),
		keyDir:     dirOf(cr),
	}

	// The cluster block. Only the deployment-wide settings live here; id/addr/zone come from env.
	cluster := map[string]any{
		keyEtcd: etcdEndpoints,
		keyPort: int(peerPortOf(cr)),
	}
	if rf := cr.Spec.Cluster.ReplicationFactor; rf != nil {
		cluster["rf"] = int(*rf)
	}
	if s := cr.Spec.Cluster.ShardsPerTenant; s != nil {
		cluster["shards_per_tenant"] = int(*s)
	}
	if p := cr.Spec.Cluster.EtcdPrefix; p != "" {
		cluster["root"] = p
	}
	storage["cluster"] = cluster

	// S3 shared backend (optional). The data dir doubles as the WAL dir so unflushed head data
	// survives restarts. Credentials are injected via env (AWS default chain), never the config.
	if backendOf(cr) == dbv1alpha1.StorageBackendS3 {
		s3 := cr.Spec.Storage.S3
		if s3 == nil || s3.Bucket == "" {
			return "", invalidSpec("spec.storage.s3.bucket is required when spec.storage.backend is s3")
		}
		m := map[string]any{
			"bucket":           s3.Bucket,
			"force_path_style": s3.ForcePathStyle,
		}
		if s3.Prefix != "" {
			m["prefix"] = s3.Prefix
		}
		if s3.Region != "" {
			m["region"] = s3.Region
		}
		if s3.Endpoint != "" {
			m["endpoint"] = s3.Endpoint
		}
		storage["s3"] = m
		storage["wal_dir"] = dirOf(cr)
	}

	// Engine tuning.
	eng := cr.Spec.Engine
	if eng.FlushInterval != nil {
		storage["flush_interval"] = eng.FlushInterval.Duration.String()
	}
	if eng.ReadCacheSize != nil {
		storage["read_cache_bytes"] = eng.ReadCacheSize.Value()
	}
	if eng.DecodeCacheSize != nil {
		storage["decode_cache_bytes"] = eng.DecodeCacheSize.Value()
	}
	if eng.DecodeMemoryLimit != nil {
		storage["decode_memory_bytes"] = eng.DecodeMemoryLimit.Value()
	}
	if eng.AggregateStats != nil {
		storage["aggregate_stats"] = *eng.AggregateStats
	}

	cfg["storage"] = storage

	// Merge user-supplied ExtraConfig over the generated config. The merge is recursive so that,
	// say, storage.policy can be added without discarding the generated storage block; paths the
	// operator owns are rejected rather than merged (see reservedConfigPaths).
	if raw := cr.Spec.ExtraConfig; raw != nil && len(raw.Raw) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(raw.Raw, &extra); err != nil {
			return "", invalidSpec("parse spec.extraConfig: %s", err)
		}
		if err := validateExtraConfig(extra); err != nil {
			return "", err
		}
		deepMerge(cfg, extra)
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(out), nil
}

func backendOf(cr *dbv1alpha1.OtelDBCluster) dbv1alpha1.StorageBackend {
	if b := cr.Spec.Storage.Backend; b != "" {
		return b
	}
	return dbv1alpha1.StorageBackendFile
}

func dirOf(cr *dbv1alpha1.OtelDBCluster) string {
	if d := cr.Spec.Storage.Dir; d != "" {
		return d
	}
	return defaultDataDir
}

func peerPortOf(cr *dbv1alpha1.OtelDBCluster) int32 {
	if p := cr.Spec.Cluster.PeerPort; p != 0 {
		return p
	}
	return portPeer
}

func replicasOf(cr *dbv1alpha1.OtelDBCluster) int32 {
	if r := cr.Spec.Replicas; r != nil {
		return *r
	}
	return 3
}

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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// OtelDBClusterSpec defines the desired state of an OtelDBCluster: a clustered deployment of
// oteldb running the embedded storage engine in cluster mode (etcd-coordinated ring, RF
// replication across peers, one symmetric StatefulSet).
type OtelDBClusterSpec struct {
	// Replicas is the number of oteldb storage nodes in the cluster. Each node is a symmetric
	// StatefulSet pod that ingests, queries, stores, and replicates. For real redundancy this
	// should be at least the replication factor (see Cluster.ReplicationFactor).
	//
	// This is the storage dimension only. Ingest and query capacity scale separately via
	// spec.ingest and spec.query, and scaling any of them has no effect on the others: membership
	// lives in etcd and the ring is computed from it, so a stateless pod needs no knowledge of how
	// many storage nodes there are.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Ingest optionally deploys a pool of stateless odbingest write nodes, so ingest capacity
	// scales independently of storage. Absent (the default) means no ingest pool: the storage
	// nodes are symmetric and accept writes themselves.
	//
	// The pool holds no data and is not a ring member. It follows etcd membership read-only and
	// routes each shard's write to that shard's primary, so scaling spec.replicas needs no change
	// to it at all.
	// +optional
	Ingest *IngestSpec `json:"ingest,omitempty"`

	// Query optionally deploys a pool of stateless odbselect query nodes, so query capacity scales
	// independently of storage. Absent (the default) means no query pool: the storage nodes are
	// symmetric and answer queries themselves.
	//
	// The pool holds no data and is not a ring member. It follows etcd membership read-only and
	// fans each read out to the shard's owners, so scaling spec.replicas needs no change to it.
	// +optional
	Query *QuerySpec `json:"query,omitempty"`

	// Image is the oteldb container image. Defaults to the operator's pinned image when empty.
	// +optional
	Image string `json:"image,omitempty"`

	// ImagePullPolicy for the oteldb container.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets are references to secrets for pulling the oteldb image.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// LogLevel sets OTEL_LOG_LEVEL for the oteldb process (e.g. DEBUG, INFO, WARN, ERROR).
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// Etcd points at the external etcd used for cluster membership, the ring, and compaction
	// claims. etcd is intentionally not managed by this operator (its backup/DR/upgrade lifecycle
	// belongs to a dedicated etcd operator or platform service); bring your own endpoints.
	// +required
	Etcd EtcdSpec `json:"etcd"`

	// Storage configures the per-node durable tier. By default each node keeps its own shards on
	// a local file backend (a per-pod PersistentVolume); cross-node RF replication provides
	// redundancy. Optionally a shared S3 object store can be used instead.
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`

	// Cluster tunes the storage cluster: replication factor, per-tenant sharding, peer port, and
	// the etcd key prefix.
	// +optional
	Cluster ClusterSpec `json:"cluster,omitempty"`

	// Signals selects which telemetry signals the cluster serves (all default to enabled and are
	// served from the embedded clustered storage engine). Disabling every signal is rejected.
	// +optional
	Signals SignalsSpec `json:"signals,omitempty"`

	// Engine tunes the embedded storage engine's caches and flush behavior.
	// +optional
	Engine EngineSpec `json:"engine,omitempty"`

	// Policy is the per-tenant storage policy: retention, admission-control limits, and the
	// merge-time downsample/precision/recompress/erasure-coding tiers. It maps onto oteldb's
	// storage.policy block. Empty leaves the engine at its defaults (retain forever, no limits,
	// lossless, raw, full-copy).
	// +optional
	Policy PolicySpec `json:"policy,omitempty"`

	// Service configures the client-facing Service that exposes the query and ingest APIs.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// Admin optionally publishes oteldb's admin API on a Service of its own. Absent — the default —
	// publishes nothing: the API is still served inside every storage pod, but no Service routes to
	// it.
	// +optional
	Admin *AdminSpec `json:"admin,omitempty"`

	// Resources are the compute resources for each oteldb container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// PodAnnotations are added to every oteldb pod.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// PodLabels are added to every oteldb pod.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// ServiceAccountName is the ServiceAccount for the oteldb pods. Empty uses the namespace
	// default.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// NodeSelector constrains oteldb pods to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Affinity for oteldb pods. When empty, the operator applies a soft anti-affinity that
	// spreads replicas across nodes.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations for oteldb pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// TopologySpreadConstraints for oteldb pods (e.g. to spread across zones for zone-aware
	// replica placement).
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// PodSecurityContext for oteldb pods.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// SecurityContext for the oteldb container.
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// ExtraConfig is arbitrary additional oteldb config deeply merged over the generated config, as
	// a top-level YAML/JSON object. Use it to set fields the CRD does not model directly (auth,
	// prometheus tuning, ...). Nested objects are merged key by key, so
	// storage.policy can be added without discarding the generated storage block; any other value
	// overrides the generated one.
	//
	// The paths the operator owns are reserved and rejected with a Degraded/InvalidSpec condition
	// instead of being merged: metrics_backend, traces_backend, logs_backend, profiles_backend,
	// storage.backend, storage.dir, storage.wal_dir, storage.s3, storage.cluster (and everything
	// below it), storage.flush_interval, storage.read_cache_bytes, storage.decode_cache_bytes,
	// storage.decode_memory_bytes, storage.aggregate_stats and storage.policy (modelled in full,
	// erasure coding included, so the whole block is reserved). Configure those through spec.storage, spec.cluster, spec.etcd,
	// spec.signals, spec.engine and spec.policy.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig *runtime.RawExtension `json:"extraConfig,omitempty"`
}

// EtcdSpec points at an external etcd coordination store.
type EtcdSpec struct {
	// Endpoints is the etcd endpoint list (e.g. ["http://etcd-0.etcd:2379", "http://etcd-1.etcd:2379"]).
	// +kubebuilder:validation:MinItems=1
	// +required
	Endpoints []string `json:"endpoints"`
}

// IngestSpec configures the stateless odbingest write pool: a Deployment of nodes that accept
// OTLP and Prometheus remote write and route each shard to its ring primary.
//
// The pool's ring parameters (etcd endpoints, replication factor, shards per tenant, key prefix)
// are rendered from spec.etcd and spec.cluster, the same source the storage nodes render from. A
// mismatch there does not fail — it resolves a different owner set than the nodes do, and writes
// land where no read will look for them — so it is deliberately not configurable per pool.
//
// Writes route to the "default" tenant unless Tenant opts the pool into tenant resolution; see the
// warning on that field before doing so.
//
// The odbingest binary is not yet shipped in the released oteldb image (it is absent from oteldb's
// goreleaser builds and release Dockerfile). Until it is, set Image to a build that contains it.
type IngestSpec struct {
	// Replicas is the number of odbingest pods. They hold no data, so this is purely a throughput
	// and availability knob and can be changed freely.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Image is the container image running odbingest. Defaults to the cluster image
	// (spec.image, else the operator's pinned image).
	// +optional
	Image string `json:"image,omitempty"`

	// Resources are the compute resources for each odbingest container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Service configures the client-facing Service that exposes the ingest endpoints.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// PodAnnotations are added to every odbingest pod.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// PodLabels are added to every odbingest pod.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// ServiceAccountName is the ServiceAccount for the odbingest pods. Empty falls back to
	// spec.serviceAccountName.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// NodeSelector constrains odbingest pods to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Affinity for odbingest pods. When empty, the operator applies a soft anti-affinity that
	// spreads the pool across nodes.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations for odbingest pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// TopologySpreadConstraints for odbingest pods.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// PodSecurityContext for odbingest pods.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// SecurityContext for the odbingest container.
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// Tenant opts the ingest pool into multi-tenant write routing. Absent — the default — routes
	// every write to the "default" tenant, exactly as an unconfigured odbingest does, and renders
	// no tenant block at all.
	//
	// Leave it absent unless you are running a deliberate migration. oteldb's *read* path is still
	// pinned to a single tenant: the query Backend's tenant id is never assigned, so every PromQL,
	// LogQL, TraceQL and Pyroscope query resolves "default" (oteldb/oteldb#820). Enabling tenant
	// resolution here therefore routes writes into tenants that nothing can currently query — the
	// data is stored, under a different shard key, and is invisible until the read path catches up.
	// The field exists so the deployment is configurable ahead of that, not because it is ready.
	// +optional
	Tenant *TenantSpec `json:"tenant,omitempty"`

	// ExtraConfig is arbitrary additional odbingest config deeply merged over the generated
	// config, as a top-level YAML/JSON object. Use it to set fields the CRD does not model, such as
	// prometheus_remote_write.time_threshold or the OTLP body-size limits.
	//
	// The cluster and tenant blocks are reserved and rejected instead of merged: cluster must stay
	// in step with the storage nodes, and tenant is modelled in full. Configure them through
	// spec.etcd, spec.cluster and spec.ingest.tenant.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig *runtime.RawExtension `json:"extraConfig,omitempty"`
}

// TenantSpec configures which tenant an ingested write routes to, mapping onto odbingest's tenant
// config block.
//
// The sources compose narrowest-first: Header names the tenant of a whole request and wins, then
// ResourceAttributes names the tenant of one resource within it, and Default backs both. A header
// beats an attribute because a gateway that authenticated the sender sets the header, whereas an
// attribute is whatever the sender put in its own payload.
//
// A tenant id picks the shard key, which picks the ring owners, so changing how tenants resolve
// moves where data lands. That makes enabling this a migration rather than a config tweak — and,
// while oteldb's read path is single-tenant (see IngestSpec.Tenant), a one-way one.
type TenantSpec struct {
	// Header is the request header — and OTLP/gRPC metadata key — carrying the tenant. Empty does
	// not read a header. "X-Scope-OrgID" is what Grafana-stack senders (Loki, Mimir, and their
	// Grafana datasources) put it in.
	// +optional
	Header string `json:"header,omitempty"`

	// ResourceAttributes are OTLP resource attribute keys, read in order until one holds a usable
	// tenant. Empty does not read resource attributes. "service.namespace" is the OTel-native
	// candidate.
	//
	// A missing, non-string or malformed value falls back to Default rather than failing the batch:
	// by the time a resource is framed, the request can no longer be answered with an error.
	// +optional
	// +listType=atomic
	ResourceAttributes []string `json:"resourceAttributes,omitempty"`

	// Default is the tenant a write routes to when no source names one. Empty uses odbingest's own
	// default tenant, "default", which is where every write goes today.
	// +optional
	Default string `json:"default,omitempty"`

	// Require refuses a request that does not carry Header with 400 (InvalidArgument over gRPC),
	// instead of routing it to Default. Use it when two senders must not be able to land in one
	// shared tenant by omitting the header. It needs Header to be set.
	// +optional
	Require bool `json:"require,omitempty"`
}

// QuerySpec configures the stateless odbselect query pool: a Deployment of nodes that serve the
// PromQL, LogQL, TraceQL and Pyroscope APIs by reading through the ring instead of local storage.
//
// The pool's ring parameters (etcd endpoints, replication factor, shards per tenant, key prefix)
// are rendered from spec.etcd and spec.cluster, the same source the storage nodes render from. A
// mismatch there does not fail — it resolves a different owner set than the nodes do, so reads look
// where the data is not — so it is deliberately not configurable per pool.
//
// Which APIs the pool serves follows spec.signals, the same toggle that decides what the storage
// nodes serve and store: a disabled signal's API is switched off on the pool and its port is
// dropped from the query Service. There is no separate per-API toggle, because serving an API for
// a signal the cluster does not store can only answer empty.
//
// The odbselect binary is not yet shipped in the released oteldb image (it lands with
// oteldb/oteldb#1266). Until it is, set Image to a build that contains it.
type QuerySpec struct {
	// Replicas is the number of odbselect pods. They hold no data, so this is purely a throughput
	// and availability knob and can be changed freely.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Image is the container image running odbselect. Defaults to the cluster image
	// (spec.image, else the operator's pinned image).
	// +optional
	Image string `json:"image,omitempty"`

	// Resources are the compute resources for each odbselect container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Service configures the client-facing Service that exposes the query APIs. One Service carries
	// every enabled API, each on its own port; they share a pod set, so splitting them across four
	// Services would only add DNS names.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// PodAnnotations are added to every odbselect pod.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// PodLabels are added to every odbselect pod.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// ServiceAccountName is the ServiceAccount for the odbselect pods. Empty falls back to
	// spec.serviceAccountName.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// NodeSelector constrains odbselect pods to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Affinity for odbselect pods. When empty, the operator applies a soft anti-affinity that
	// spreads the pool across nodes.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations for odbselect pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// TopologySpreadConstraints for odbselect pods.
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// PodSecurityContext for odbselect pods.
	// +optional
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`

	// SecurityContext for the odbselect container.
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// ExtraConfig is arbitrary additional odbselect config deeply merged over the generated config,
	// as a top-level YAML/JSON object. Use it to set fields the CRD does not model, such as
	// prometheus.max_samples, loki.max_sample_rows, per-listener auth or shutdown_timeout.
	//
	// The cluster block is reserved and rejected instead of merged: it must stay in step with the
	// storage nodes. Configure it through spec.etcd and spec.cluster. The listener addresses
	// (prometheus.bind, loki.bind, tempo.bind, pyroscope.bind, health.bind) are reserved too: the
	// operator publishes them as Service ports and probes them, and odbselect keeps serving its
	// other listeners if one moves, so a rebind would fail silently rather than crash the pod. Use
	// spec.signals to turn an API off.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig *runtime.RawExtension `json:"extraConfig,omitempty"`
}

// StorageBackend selects the embedded storage engine's durable backend.
// +kubebuilder:validation:Enum=file;s3
type StorageBackend string

const (
	// StorageBackendFile is the per-node local file backend (the default, shared-nothing model:
	// each node stores its own shards and RF replication provides redundancy).
	StorageBackendFile StorageBackend = "file"
	// StorageBackendS3 is a shared S3-compatible object store.
	StorageBackendS3 StorageBackend = "s3"
)

// StorageSpec configures the per-node durable tier.
type StorageSpec struct {
	// Backend selects the durable backend: "file" (default, per-node local disk) or "s3" (shared
	// object store).
	// +kubebuilder:default=file
	// +optional
	Backend StorageBackend `json:"backend,omitempty"`

	// Dir is the data directory mounted in each pod for the file backend (parts + WAL). It is
	// also used as the WAL directory for the s3 backend so unflushed head data survives restarts.
	// +kubebuilder:default="/var/lib/oteldb"
	// +optional
	Dir string `json:"dir,omitempty"`

	// Size is the size of each node's data PersistentVolume.
	// +kubebuilder:default="50Gi"
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// StorageClassName for the data PersistentVolumeClaims. Empty uses the cluster default.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessModes for the data PersistentVolumeClaims.
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// S3 configures the shared S3 object store. Required when Backend is "s3".
	// +optional
	S3 *S3Spec `json:"s3,omitempty"`
}

// S3Spec configures a shared S3-compatible object store backend.
type S3Spec struct {
	// Bucket holding the data. Required for the s3 backend.
	Bucket string `json:"bucket"`

	// Prefix is an optional root key prefix so several datasets can share one bucket.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Region is the S3 region. Empty resolves from the environment/credential chain.
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint overrides the S3 endpoint URL for S3-compatible stores (e.g. "http://minio:9000").
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ForcePathStyle addresses objects as endpoint/bucket/key. Required by most S3-compatible
	// stores (MinIO, Ceph).
	// +optional
	ForcePathStyle bool `json:"forcePathStyle,omitempty"`

	// CredentialsSecret references a Secret holding the access key id and secret access key. They
	// are injected into the pods as AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (the AWS default
	// credential chain), keeping credentials out of the ConfigMap. When unset, the pods' ambient
	// credentials (IRSA/instance role) are used.
	// +optional
	CredentialsSecret *S3CredentialsSecret `json:"credentialsSecret,omitempty"`
}

// S3CredentialsSecret references the keys of a Secret holding S3 static credentials.
type S3CredentialsSecret struct {
	// Name of the Secret.
	Name string `json:"name"`

	// AccessKeyIDKey is the Secret key holding the access key id.
	// +kubebuilder:default="AWS_ACCESS_KEY_ID"
	// +optional
	AccessKeyIDKey string `json:"accessKeyIDKey,omitempty"`

	// SecretAccessKeyKey is the Secret key holding the secret access key.
	// +kubebuilder:default="AWS_SECRET_ACCESS_KEY"
	// +optional
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`
}

// ClusterSpec tunes the storage cluster distribution layer.
type ClusterSpec struct {
	// ReplicationFactor is the number of replicas per write (RF). Should be <= Replicas. The
	// storage default is 3.
	// +kubebuilder:validation:Minimum=1
	// +optional
	ReplicationFactor *int32 `json:"replicationFactor,omitempty"`

	// ShardsPerTenant splits each tenant's series across this many independently-placed shards.
	// Zero or one keeps a single shard per tenant.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ShardsPerTenant *int32 `json:"shardsPerTenant,omitempty"`

	// PrivateBackend declares that every node's durable backend is its own, unshared by peers.
	// It gates cluster/partsync: only a private backend replicates *flushed parts* between nodes
	// and backfills a node that lost its disk. Without it the ring replicates just the in-memory
	// head, so each flushed part exists in exactly one copy regardless of ReplicationFactor.
	//
	// Unset derives from Storage.Backend: "file" is per-node PVC and therefore private (true),
	// "s3" is a shared bucket (false). Set explicitly only to override that — e.g. a per-node
	// S3 bucket, or a "file" backend on a ReadWriteMany volume shared by all pods.
	// +optional
	PrivateBackend *bool `json:"privateBackend,omitempty"`

	// PeerPort is the port peers use to reach each node's replication server.
	// +kubebuilder:default=7946
	// +optional
	PeerPort int32 `json:"peerPort,omitempty"`

	// EtcdPrefix is the etcd key prefix for this cluster's state (the storage "root"). Empty uses
	// the storage default "/oteldb".
	// +optional
	EtcdPrefix string `json:"etcdPrefix,omitempty"`

	// StaticZone sets a fixed failure domain label for every node in this cluster. The storage
	// ring spreads a key's replicas across distinct zones; set this per-cluster when a whole
	// OtelDBCluster occupies one failure domain within a larger federation, or for testing.
	// For physically spreading pods across zones, use TopologySpreadConstraints.
	// +optional
	StaticZone string `json:"staticZone,omitempty"`
}

// SignalsSpec selects which signals the cluster serves. Disabling a signal drops its backend and
// its API bind from the rendered config, and its ports from the pods and the client Service. At
// least one signal must stay enabled. Unset means enabled.
type SignalsSpec struct {
	// Metrics serves the Prometheus query API and remote-write ingest.
	// +kubebuilder:default=true
	// +optional
	Metrics *bool `json:"metrics,omitempty"`

	// Logs serves the Loki query API.
	// +kubebuilder:default=true
	// +optional
	Logs *bool `json:"logs,omitempty"`

	// Traces serves the Tempo query API.
	// +kubebuilder:default=true
	// +optional
	Traces *bool `json:"traces,omitempty"`

	// Profiles serves the Pyroscope query and ingest API.
	// +kubebuilder:default=true
	// +optional
	Profiles *bool `json:"profiles,omitempty"`
}

// EngineSpec tunes the embedded storage engine.
type EngineSpec struct {
	// FlushInterval is the max age of unflushed head data before it is flushed to a part.
	// +optional
	FlushInterval *metav1.Duration `json:"flushInterval,omitempty"`

	// ReadCacheSize sizes the in-memory object read cache (cold-tier LRU). Empty uses the oteldb
	// auto-sizing; "0" disables it.
	// +optional
	ReadCacheSize *resource.Quantity `json:"readCacheSize,omitempty"`

	// DecodeCacheSize sizes the per-tenant decoded-column cache. Empty uses auto-sizing; "0"
	// disables it.
	// +optional
	DecodeCacheSize *resource.Quantity `json:"decodeCacheSize,omitempty"`

	// DecodeMemoryLimit caps in-flight decoded column bytes across concurrent queries. Empty uses
	// auto-sizing; "0" disables it.
	// +optional
	DecodeMemoryLimit *resource.Quantity `json:"decodeMemoryLimit,omitempty"`

	// AggregateStats writes a per-series aggregate sidecar so range aggregates are answered
	// without decoding. Defaults to the engine default (enabled).
	// +optional
	AggregateStats *bool `json:"aggregateStats,omitempty"`
}

// PolicySpec is the per-tenant storage policy, mapping 1:1 onto oteldb's storage.policy block.
//
// Downsample, Precision and Recompress work against oteldb v0.48.0. Retention and Limits landed in
// oteldb's config after it; older builds ignore unknown config keys silently, so against those
// those two are accepted and do nothing — pin a newer Image before relying on them.
type PolicySpec struct {
	// Retention bounds how long ingested data is kept. Empty retains forever.
	// +optional
	Retention RetentionSpec `json:"retention,omitempty"`

	// Limits are the per-node admission-control limits. Empty means unlimited.
	// +optional
	Limits LimitsSpec `json:"limits,omitempty"`

	// Downsample is the age-tiered merge-time rollup: samples older than a tier's After are
	// replaced by one representative per Interval-wide bucket. Empty keeps data raw.
	//
	// This rewrites data in place and cannot be undone: lowering a tier's After re-processes
	// existing parts at the next merge, and the replaced samples are gone.
	// +optional
	// +listType=atomic
	Downsample []DownsampleTierSpec `json:"downsample,omitempty"`

	// Precision is the age-tiered lossy float-compression policy: the value column of parts older
	// than a tier's After is re-encoded to keep only Bits mantissa bits. Empty stays lossless.
	//
	// This rewrites data in place and cannot be undone: discarded mantissa bits are not
	// recoverable, and lowering a tier's After re-processes existing parts at the next merge.
	// +optional
	// +listType=atomic
	Precision []PrecisionTierSpec `json:"precision,omitempty"`

	// Recompress rewrites fully-cold parts with a higher-ratio Zstandard profile at merge, trading
	// merge CPU for storage. It is decode-transparent and lossless. Nil disables it.
	// +optional
	Recompress *RecompressSpec `json:"recompress,omitempty"`

	// EC erasure-codes fully-cold parts across Data+Parity nodes instead of holding RF full copies.
	// Nil keeps full-copy replication.
	//
	// It applies only to a shared-nothing cluster — cluster mode with a private per-node backend —
	// which is exactly what this operator deploys by default, and is the only deployment shape in
	// which erasure coding is reachable at all. On a shared object store the store owns durability
	// and the engine leaves every part full-copy, so the operator rejects the block there instead
	// of rendering a policy that costs and saves nothing.
	// +optional
	EC *ECSpec `json:"ec,omitempty"`
}

// DownsampleTierSpec is one age band of the downsampling policy. Tiers are order-independent: a
// sample is rolled up by the coarsest tier whose After it has exceeded, and samples younger than
// every tier stay raw. Buckets align to absolute multiples of Interval, so repeated merges are
// stable.
type DownsampleTierSpec struct {
	// After is the age past which this tier applies, relative to merge time (e.g. "24h").
	// +required
	After metav1.Duration `json:"after"`

	// Interval is the rollup bucket width (e.g. "5m"). It must be positive.
	// +required
	Interval metav1.Duration `json:"interval"`

	// Agg combines the samples in a bucket. Defaults to "last".
	// +kubebuilder:validation:Enum=last;first;min;max;sum;avg;count
	// +optional
	Agg string `json:"agg,omitempty"`
}

// PrecisionTierSpec is one age band of the lossy float-precision policy. Tiers are
// order-independent: a part takes the most aggressive tier whose After it has exceeded. The
// encoder keeps whichever of the lossy and lossless encodings is smaller, so a tier can only help
// size.
type PrecisionTierSpec struct {
	// After is the age past which this tier applies, relative to merge time (e.g. "168h").
	// +required
	After metav1.Duration `json:"after"`

	// Bits is the number of significant mantissa bits retained. Fewer bits compress better and
	// lose more accuracy.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=63
	// +required
	Bits int32 `json:"bits"`
}

// RecompressSpec configures cold-part recompression.
type RecompressSpec struct {
	// After is the age past which a fully-cold part is recompressed at merge. It must be positive
	// — the block exists only to enable recompression.
	// +required
	After metav1.Duration `json:"after"`

	// Level is the Zstandard level: 1 is fastest, 19 is the best ratio. Empty uses the best-ratio
	// default.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=19
	// +optional
	Level *int32 `json:"level,omitempty"`
}

// ECSpec configures the cold-data erasure-coding tier: a flushed part older than After is
// re-encoded at merge into Data + Parity Reed-Solomon shards, one per cluster node, in place of the
// RF full copies. It stores (Data+Parity)/Data of the logical bytes and survives Parity node
// losses — {4,2} is 1.5x for two tolerated losses, against 3x for RF=3 — paying a
// reconstruct-on-read cost only for the parts that have converted.
//
// Data+Parity becomes the tenant's owner count, so it must not exceed Replicas, and it replaces
// spec.cluster.replicationFactor outright rather than combining with it (see ECSpec.Parity).
type ECSpec struct {
	// Data is the number of data shards (k): any Data shards reconstruct the object.
	// +kubebuilder:validation:Minimum=1
	// +required
	Data int32 `json:"data"`

	// Parity is the number of parity shards (m): the scheme tolerates Parity node losses.
	//
	// Data+Parity is also the tenant's owner count under erasure coding, and it applies to the
	// unflushed head too, not only to the converted parts: the storage engine's replication-factor
	// lookup returns Data+Parity and ignores the configured replication factor entirely. An EC
	// policy therefore silently overrides spec.cluster.replicationFactor, so the operator rejects
	// setting both instead of honouring one and dropping the other.
	//
	// Shard placement is rack-safe — one zone failure costs at most Parity shards — only with at
	// least ceil((Data+Parity)/Parity) distinct zones; below that the engine still converts, and
	// warns. Spread the nodes with spec.topologySpreadConstraints to earn that.
	// +kubebuilder:validation:Minimum=1
	// +required
	Parity int32 `json:"parity"`

	// After is the age past which a fully-cold part is erasure-coded at merge, mirroring
	// RecompressSpec.After and riding the same background merge. Empty erasure-codes every part,
	// accepting the reconstruct-on-read cost everywhere for the cheapest storage.
	// +optional
	After *metav1.Duration `json:"after,omitempty"`
}

// RetentionSpec bounds how long data is kept. Enforcement happens at merge time and drops whole
// partitions — never individual rows — so data can outlive the window until the partition holding
// it has fully expired.
type RetentionSpec struct {
	// MaxAge is the maximum age of retained data (e.g. "720h"). Empty retains forever.
	// +optional
	MaxAge *metav1.Duration `json:"maxAge,omitempty"`

	// MaxBytes is the total retained-bytes budget across every signal on a node.
	//
	// oteldb accepts it, but the storage engine does not enforce it yet (oteldb/storage#224), so
	// setting it alone bounds nothing today. Use MaxAge to bound disk growth.
	// +optional
	MaxBytes *resource.Quantity `json:"maxBytes,omitempty"`
}

// LimitsSpec are the per-node admission-control limits. They shed over-budget writes and report
// them as OTLP partial success (RESOURCE_EXHAUSTED), so an overload degrades rather than OOMs.
type LimitsSpec struct {
	// IngestBytesPerSecond caps the ingest rate, bursting to one second of budget.
	// +optional
	IngestBytesPerSecond *resource.Quantity `json:"ingestBytesPerSecond,omitempty"`

	// MaxInFlightBytes caps the unflushed in-flight bytes buffered before backpressure sheds.
	// +optional
	MaxInFlightBytes *resource.Quantity `json:"maxInFlightBytes,omitempty"`

	// MaxSeries is the hard active-series ceiling: a sample minting a new series past it is shed.
	// Existing series are unaffected.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxSeries *int64 `json:"maxSeries,omitempty"`

	// MaxSeriesSoft is a soft cardinality budget (metrics only): past it a new series' samples go
	// to a synthetic per-metric overflow series instead of being shed, until MaxSeries is reached.
	// It must not exceed MaxSeries, and needs MaxSeries set to have any effect.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxSeriesSoft *int64 `json:"maxSeriesSoft,omitempty"`

	// MaxPartSize caps an immutable part's approximate uncompressed size; flush and merge split
	// their output to respect it. It is structural: fixed when a node's engine is first created,
	// so changing it does not affect existing data.
	// +optional
	MaxPartSize *resource.Quantity `json:"maxPartSize,omitempty"`
}

// AdminSpec publishes oteldb's admin API on a dedicated <name>-admin Service.
//
// The API is not optional inside the pod: oteldb registers it unconditionally and its bind defaults
// to :8090, so every storage node is already serving it. What was missing is a way to reach it —
// before the self-metrics port moved to 9464 it happened to be published under a port named
// "metrics", and moving that port took the accidental exposure with it. Neither state was a
// decision; this field is.
//
// It is a separate Service, and opt-in, because of what the API can do: it triggers the engine's
// maintenance and compaction passes, and serves the stream-cost attribution report — documented
// upstream as the heaviest call the storage library exposes, decoding every accounted byte column
// of every live part. None of that belongs on the client Service that PromQL and OTLP share, where
// an ingress or a broad NetworkPolicy would pick it up by default.
//
// A separate Service also keeps the exposure decision separate from the reachability decision: the
// admin Service can stay ClusterIP while the client Service is a LoadBalancer, and it can carry its
// own annotations (auth proxy, internal-only load balancer) without touching client traffic.
//
// oteldb has no auth on the admin API beyond the global spec.extraConfig auth block, so treat this
// Service as privileged and restrict it with a NetworkPolicy.
type AdminSpec struct {
	// Service configures the <name>-admin Service. Its default type is ClusterIP, which is the
	// intended shape: the admin API is an operator tool, not a client-facing endpoint.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`
}

// ServiceSpec configures the client-facing Service.
type ServiceSpec struct {
	// Type of the client Service.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Annotations added to the client Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// OtelDBClusterPhase is a coarse lifecycle phase for display.
// +kubebuilder:validation:Enum=Pending;Progressing;Ready;Degraded
type OtelDBClusterPhase string

const (
	// PhasePending means the cluster's dependencies are not yet satisfied.
	PhasePending OtelDBClusterPhase = "Pending"
	// PhaseProgressing means the cluster is scaling or rolling out.
	PhaseProgressing OtelDBClusterPhase = "Progressing"
	// PhaseReady means all replicas are ready.
	PhaseReady OtelDBClusterPhase = "Ready"
	// PhaseDegraded means the cluster failed to reach or maintain its desired state.
	PhaseDegraded OtelDBClusterPhase = "Degraded"
)

// Condition types set by the controller.
const (
	// ConditionAvailable is True when the cluster is serving (at least one node ready).
	ConditionAvailable = "Available"
	// ConditionProgressing is True while a rollout/scale is in progress.
	ConditionProgressing = "Progressing"
	// ConditionDegraded is True when reconciliation failed.
	ConditionDegraded = "Degraded"
)

// OtelDBClusterStatus defines the observed state of OtelDBCluster.
type OtelDBClusterStatus struct {
	// Phase is a coarse, human-readable lifecycle phase.
	// +optional
	Phase OtelDBClusterPhase `json:"phase,omitempty"`

	// Replicas is the desired number of oteldb nodes.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready oteldb nodes.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// IngestReplicas is the desired number of odbingest nodes. Zero when no ingest pool is
	// configured.
	// +optional
	IngestReplicas int32 `json:"ingestReplicas,omitempty"`

	// IngestReadyReplicas is the number of ready odbingest nodes.
	// +optional
	IngestReadyReplicas int32 `json:"ingestReadyReplicas,omitempty"`

	// QueryReplicas is the desired number of odbselect nodes. Zero when no query pool is
	// configured.
	// +optional
	QueryReplicas int32 `json:"queryReplicas,omitempty"`

	// QueryReadyReplicas is the number of ready odbselect nodes.
	// +optional
	QueryReadyReplicas int32 `json:"queryReadyReplicas,omitempty"`

	// EtcdEndpoints is the resolved etcd endpoint list the cluster is using.
	// +optional
	EtcdEndpoints []string `json:"etcdEndpoints,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the OtelDBCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=odb;oteldb
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OtelDBCluster is the Schema for the oteldbclusters API: a clustered, etcd-coordinated oteldb
// deployment backed by the embedded storage engine.
type OtelDBCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of OtelDBCluster
	// +required
	Spec OtelDBClusterSpec `json:"spec"`

	// status defines the observed state of OtelDBCluster
	// +optional
	Status OtelDBClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OtelDBClusterList contains a list of OtelDBCluster
type OtelDBClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OtelDBCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &OtelDBCluster{}, &OtelDBClusterList{})
		return nil
	})
}

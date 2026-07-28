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
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

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

	// Policy is the per-tenant storage policy: retention and admission-control limits. It maps
	// onto oteldb's storage.policy block. Empty leaves the engine at its defaults (retain
	// forever, no limits).
	// +optional
	Policy PolicySpec `json:"policy,omitempty"`

	// Service configures the client-facing Service that exposes the query and ingest APIs.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

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
	// prometheus tuning, retention policy, ...). Nested objects are merged key by key, so
	// storage.policy can be added without discarding the generated storage block; any other value
	// overrides the generated one.
	//
	// The paths the operator owns are reserved and rejected with a Degraded/InvalidSpec condition
	// instead of being merged: metrics_backend, traces_backend, logs_backend, profiles_backend,
	// storage.backend, storage.dir, storage.wal_dir, storage.s3, storage.cluster (and everything
	// below it), storage.flush_interval, storage.read_cache_bytes, storage.decode_cache_bytes,
	// storage.decode_memory_bytes, storage.aggregate_stats, storage.policy.retention and
	// storage.policy.limits. Configure those through spec.storage, spec.cluster, spec.etcd,
	// spec.signals, spec.engine and spec.policy. The rest of storage.policy
	// (precision, downsample, recompress) stays mergeable.
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
// Retention and limits landed in oteldb's config after v0.48.0. Older builds ignore unknown config
// keys silently, so against those this policy is accepted and does nothing — pin a newer Image
// before relying on it.
//
// The merge-time policies oteldb also supports there — precision, downsample and recompress — are
// not modelled yet (see oteldb/operator#3); they stay reachable through spec.extraConfig.
type PolicySpec struct {
	// Retention bounds how long ingested data is kept. Empty retains forever.
	// +optional
	Retention RetentionSpec `json:"retention,omitempty"`

	// Limits are the per-node admission-control limits. Empty means unlimited.
	// +optional
	Limits LimitsSpec `json:"limits,omitempty"`
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

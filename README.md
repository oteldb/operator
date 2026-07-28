# oteldb-operator

A Kubernetes operator (built with [Kubebuilder](https://book.kubebuilder.io/)) that runs
**clustered [oteldb](https://github.com/oteldb/oteldb)** — the OpenTelemetry observability backend —
using the embedded [`github.com/oteldb/storage`](https://github.com/oteldb/storage) engine in its
distributed cluster mode.

## What "clustered" means here

oteldb is a **single, symmetric binary**: every node ingests (OTLP / Prometheus remote-write),
serves the query APIs (PromQL, LogQL, TraceQL, Pyroscope), stores data, and replicates. There is no
distributor/ingester/querier split. Clustering is provided by the storage engine's L0 layer:

- **etcd** coordinates membership (leases), the rendezvous-hash (HRW) ring, and compaction claims.
- Nodes form a ring and **replicate the unflushed write head to `RF` peers over HTTP** (peer port
  `7946`), quorum-acked and primary-authoritative.
- **Durable tier defaults to a per-node local `file` backend** (one PersistentVolume per pod) —
  the shared-nothing model where cross-node RF replication provides redundancy. A shared
  **S3-compatible object store** is optional (`storage.backend: s3`).

The operator models this as **one StatefulSet** of oteldb pods (stable identity → ring id, stable
FQDN → peer address, per-pod PVC), a **headless Service** for peer DNS, and a **client Service** for
the query/ingest APIs.

### etcd is bring-your-own

The operator **does not manage etcd**. etcd's real operational needs — backups, disaster recovery,
defrag, TLS/cert rotation, careful upgrades — belong to a dedicated etcd operator or a managed
service. Point `spec.etcd.endpoints` at your etcd. For local testing only, apply
[`config/samples/etcd-dev.yaml`](config/samples/etcd-dev.yaml) (single-replica, emptyDir, no TLS).

## The API: `OtelDBCluster`

`db.oteldb.io/v1alpha1`, `Kind: OtelDBCluster` (short names `odb`, `oteldb`).

```yaml
apiVersion: db.oteldb.io/v1alpha1
kind: OtelDBCluster
metadata:
  name: obs
spec:
  replicas: 5                       # symmetric oteldb nodes (>= replicationFactor)
  etcd:
    endpoints: ["http://etcd:2379"] # required, external
  storage:
    backend: file                   # default per-node local disk; or "s3"
    dir: /var/lib/oteldb
    size: 100Gi                     # per-pod PVC
  cluster:
    replicationFactor: 3            # RF replicas per write
    shardsPerTenant: 4              # spread a tenant's series across placement units
    peerPort: 7946
  signals: { metrics: true, logs: true, traces: true, profiles: true }
```

See [`config/samples/db_v1alpha1_oteldbcluster.yaml`](config/samples/db_v1alpha1_oteldbcluster.yaml)
for a fuller example including the S3 backend.

### Key spec fields

| Field | Purpose |
|---|---|
| `replicas` | Number of oteldb nodes (StatefulSet size). Use `>= cluster.replicationFactor`. |
| `image` / `imagePullPolicy` / `imagePullSecrets` | oteldb container image (default `ghcr.io/oteldb/oteldb:v0.46.0`). |
| `etcd.endpoints` | **Required.** External etcd endpoint list. |
| `storage.backend` | `file` (default, per-node PVC) or `s3` (shared object store). |
| `storage.dir` / `size` / `storageClassName` / `accessModes` | Per-pod data volume (parts + WAL). |
| `storage.s3` | Bucket/endpoint/region/forcePathStyle + `credentialsSecret` (injected via the AWS default credential chain, never the ConfigMap). |
| `cluster.replicationFactor` | Replicas per write (RF). |
| `cluster.shardsPerTenant` | Per-tenant series sharding across placement units. |
| `cluster.peerPort` | Peer replication port (default 7946). |
| `cluster.etcdPrefix` | etcd key prefix (storage "root", default `/oteldb`). |
| `cluster.staticZone` | Fixed failure-domain label for the cluster's nodes (ring zone-spreading). |
| `signals` | Which signals to serve (all default on). Disabling one drops its backend, its API bind and its ports; disabling all is rejected. |
| `engine` | Storage engine tuning: `flushInterval`, `readCacheSize`, `decodeCacheSize`, `decodeMemoryLimit`, `aggregateStats`. |
| `service.type` / `annotations` | Client Service exposing the query/ingest APIs. |
| `resources`, `nodeSelector`, `affinity`, `tolerations`, `topologySpreadConstraints`, `podSecurityContext`, `securityContext`, `podAnnotations`, `podLabels`, `serviceAccountName` | Standard pod scheduling/security knobs. |
| `extraConfig` | Arbitrary raw oteldb config merged over the generated config (top-level keys win) — for fields the CRD does not model (auth, retention policy, prometheus tuning, …). |

### Status

`status` reports `phase` (`Pending`/`Progressing`/`Ready`/`Degraded`), `replicas`,
`readyReplicas`, the resolved `etcdEndpoints`, and standard `Available`/`Progressing`/`Degraded`
conditions. `kubectl get oteldbcluster` prints replicas, ready count, and phase.

## How per-pod identity works

The whole StatefulSet shares one rendered config ConfigMap. Each pod's **ring id** and
**peer-reachable address** are injected via environment variables the oteldb server reads
(`OTELDB_CLUSTER_ID`, `OTELDB_CLUSTER_ADDR`, and optionally `OTELDB_CLUSTER_ZONE` /
`OTELDB_CLUSTER_ZONE_FILE`): the operator sets `OTELDB_CLUSTER_ID=$(POD_NAME)` and
`OTELDB_CLUSTER_ADDR=$(POD_NAME).<name>-peers.<ns>.svc.cluster.local:7946`. This support was added
to oteldb's config loader (`cmd/oteldb/config.go`) so a clustered deployment needs no per-pod
config file.

## Ports

Client APIs (exposed by the client Service): `4317` OTLP gRPC, `4318` OTLP HTTP, `19291` Prometheus
remote-write, `9090` PromQL, `3200` TraceQL (Tempo), `3100` LogQL (Loki), `4040` Pyroscope, `8090`
self-metrics, `13133` health. Peer replication: `7946` (headless Service).

## Getting Started

Prerequisites: Go 1.26+, a Kubernetes cluster, `kubectl`, and (for building the image) Docker.

```sh
# 1. Install the CRD.
make install

# 2. (dev only) an etcd to point at.
kubectl apply -f config/samples/etcd-dev.yaml

# 3. Run the controller locally against your kubeconfig...
make run
#    ...or deploy it to the cluster:
make docker-build docker-push IMG=<registry>/oteldb-operator:tag
make deploy IMG=<registry>/oteldb-operator:tag

# 4. Create a cluster.
kubectl apply -f config/samples/db_v1alpha1_oteldbcluster.yaml
kubectl get oteldbcluster
```

Uninstall with `make undeploy` / `make uninstall`.

## Development

```sh
make manifests generate   # regenerate CRDs, RBAC, and deepcopy after editing api/
make test                 # unit tests + envtest integration tests
make lint
```

Controller layout under `internal/controller/`:
- `oteldbcluster_controller.go` — reconcile loop, apply/owner-ref helper, status.
- `config.go` — renders the oteldb `config.yml` from the spec.
- `resources.go` — builds the ConfigMap, headless + client Services, and StatefulSet.
- `naming.go` — names, labels, ports.

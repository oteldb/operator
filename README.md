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

### Optional role node groups

Symmetric nodes couple ingest and query capacity to storage capacity: the only way to absorb a write
spike or a dashboard reload is to add a node, and that node immediately takes ownership of a share
of the shards. `spec.ingest` and `spec.query` break the coupling by deploying pools of stateless
**`odbingest`** write nodes and **`odbselect`** query nodes as separate Deployments. Both are opt-in
— omit the stanzas and the cluster stays symmetric, exactly as before.

Unlike VictoriaMetrics' `vminsert`/`vmselect`, a stateless oteldb pod is **not** handed a list of
storage nodes. It watches etcd for membership and computes shard owners locally from the
rendezvous-hash ring, so `spec.replicas`, `spec.ingest.replicas` and `spec.query.replicas` are
genuinely independent: scaling storage triggers no config re-render and no restart of either pool.

What a pool *does* need is the ring's shape — `replicationFactor`, `shardsPerTenant`, `etcdPrefix`
— and a mismatch there **fails silently**: the pool resolves a different owner set than the nodes
do, so writes land where no read will look for them and reads look where the data is not. The
operator renders all three roles from the same `spec.cluster`, so the mismatch cannot be expressed.

> **Neither binary is in the released oteldb image yet.** `odbingest` is absent from oteldb's
> goreleaser builds and `release.Dockerfile`, and `odbselect`'s release packaging ships with
> [oteldb/oteldb#1266](https://github.com/oteldb/oteldb/pull/1266). No published
> `ghcr.io/oteldb/oteldb` tag contains either one. Set `spec.ingest.image` / `spec.query.image` to a
> build that ships them until that is fixed upstream.

#### Ingest tenancy

`spec.ingest.tenant` maps onto odbingest's `tenant` block
([oteldb/oteldb#1280](https://github.com/oteldb/oteldb/pull/1280)) and resolves the tenant a write
routes to. The sources compose narrowest-first — `header` (a whole request) beats
`resourceAttributes` (one resource within it) beats `default`:

```yaml
spec:
  ingest:
    tenant:
      header: X-Scope-OrgID          # what Grafana-stack senders use
      resourceAttributes: [service.namespace]
      default: shared
      require: false                 # 400 a request with no header instead of using default
```

> **Leave it absent unless you are running a deliberate migration.** oteldb's *read* path is still
> pinned to one tenant: the query `Backend`'s tenant id is never assigned, so every PromQL, LogQL,
> TraceQL and Pyroscope query resolves `"default"`
> ([oteldb/oteldb#820](https://github.com/oteldb/oteldb/issues/820)). Turning tenant resolution on
> at ingest today routes writes into tenants **nothing can query** — the data is stored, under a
> different shard key, and is invisible until the read path catches up. The field exists so the
> deployment is configurable ahead of that, not because it is ready.

Absent renders no `tenant` block at all, which is byte-identical to the config an ingest pool got
before this field existed: odbingest installs no resolver and every write lands in `"default"`.

Because a tenant id picks the shard key, which picks the ring owners, changing how tenants resolve
moves where data lands — a migration, not a config tweak. The operator validates what odbingest
would otherwise crash-loop on (`require` without `header`, an empty attribute key, a `default` that
is not a legal tenant id, a block naming no source at all) and reports it as `Degraded`/`InvalidSpec`.

#### The query pool's shape

`odbingest` serves OTLP/HTTP, remote write and its health endpoints on one listener. `odbselect`
does not: it serves **four query APIs on four listeners** (`9090` PromQL, `3100` LogQL, `3200`
TraceQL, `4040` Pyroscope) plus health on `13133`. Three consequences:

- **One Service, several ports.** `<name>-query` publishes every enabled API. The four APIs are one
  pod set behind one selector and each already has its own port, so a Service per API would add DNS
  names — and, at `type: LoadBalancer`, four load balancers — without making anything addressable
  that is not addressable now.
- **`spec.signals` turns an API off, not a per-API knob.** The four APIs map 1:1 onto the four
  signals the cluster already toggles, and serving an API for a signal the cluster does not store
  can only answer empty. A disabled signal renders `bind: "-"` (odbselect's disable convention) and
  drops the port from the pool and its Service. Note the `"-"` is *required*: odbselect defaults
  every API block it does not find, so simply omitting one would serve it anyway.
- **Probes hit the health listener**, not a data port — it is the one listener that is served
  whatever `spec.signals` leaves enabled. `/readyz` answers 503 until the ring has a member, so a
  starting pod stays out of the load balancer while a query could only return an empty result that
  looks like an answer.

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
| `replicas` | Number of oteldb storage nodes (StatefulSet size). Use `>= cluster.replicationFactor`. |
| `ingest` | Optional stateless `odbingest` write pool (Deployment). Absent ⇒ symmetric nodes handle ingest. Takes `replicas` (default 2), `image`, `service`, `tenant`, `extraConfig` and the standard scheduling/security knobs. |
| `ingest.tenant` | Opt-in multi-tenant write routing: `{header, resourceAttributes, default, require}`. **Absent by default, and that is the recommended setting** — see [Ingest tenancy](#ingest-tenancy). |
| `query` | Optional stateless `odbselect` query pool (Deployment). Absent ⇒ symmetric nodes answer queries. Same knobs as `ingest`. Which APIs it serves follows `spec.signals`. |
| `image` / `imagePullPolicy` / `imagePullSecrets` | oteldb container image (default `ghcr.io/oteldb/oteldb:v0.46.0`). |
| `etcd.endpoints` | **Required.** External etcd endpoint list. |
| `storage.backend` | `file` (default, per-node PVC) or `s3` (shared object store). |
| `storage.dir` / `size` / `storageClassName` / `accessModes` | Per-pod data volume (parts + WAL). |
| `storage.s3` | Bucket/endpoint/region/forcePathStyle + `credentialsSecret` (injected via the AWS default credential chain, never the ConfigMap). |
| `cluster.replicationFactor` | Replicas per write (RF). |
| `cluster.shardsPerTenant` | Per-tenant series sharding across placement units. |
| `cluster.peerPort` | Peer replication port (default 7946). |
| `cluster.privateBackend` | Whether each node's durable backend is its own. Gates `cluster/partsync`, which replicates *flushed parts* and backfills a node that lost its disk — without it only the in-memory head is replicated and every part exists in one copy regardless of RF. Unset derives from `storage.backend`: `true` for `file` (per-pod PVC), `false` for `s3` (shared bucket). Needs an oteldb carrying [oteldb/oteldb#1264](https://github.com/oteldb/oteldb/pull/1264); older builds ignore the key silently. |
| `cluster.etcdPrefix` | etcd key prefix (storage "root", default `/oteldb`). |
| `cluster.staticZone` | Fixed failure-domain label for the cluster's nodes (ring zone-spreading). |
| `signals` | Which signals to serve (all default on). Disabling one drops its backend, its API bind and its ports; disabling all is rejected. |
| `engine` | Storage engine tuning: `flushInterval`, `readCacheSize`, `decodeCacheSize`, `decodeMemoryLimit`, `aggregateStats`. |
| `policy.retention.maxAge` | How long data is kept (e.g. `720h`). Empty retains forever. Enforced at merge time by dropping whole partitions, so data can outlive the window briefly. |
| `policy.retention.maxBytes` | Retained-bytes budget. **Accepted but not enforced yet** by the storage engine ([oteldb/storage#224](https://github.com/oteldb/storage/issues/224)) — use `maxAge` to bound disk growth. |
| `policy.limits` | Per-node admission control: `ingestBytesPerSecond`, `maxInFlightBytes`, `maxSeries`, `maxSeriesSoft`, `maxPartSize`. Over-budget writes are shed as OTLP partial success rather than buffered. |
| `policy.downsample[]` | Merge-time age-tiered rollup: `{after, interval, agg}`. Samples past `after` collapse to one per `interval` bucket. **Lossy and irreversible.** |
| `policy.precision[]` | Age-tiered lossy float precision: `{after, bits}`. Parts past `after` keep only `bits` mantissa bits. **Lossy and irreversible.** |
| `policy.recompress` | `{after, level}`. Rewrites fully-cold parts with a higher-ratio Zstandard profile. Decode-transparent and lossless. |
| `policy.ec` | `{data, parity, after}`. Erasure-codes fully-cold parts across `data+parity` nodes instead of RF full copies — `{4,2}` is 1.5x the logical bytes for two tolerated node losses, against 3x for RF=3. See [Erasure coding](#erasure-coding). |
| `service.type` / `annotations` | Client Service exposing the query/ingest APIs. |
| `admin` | Optional `<name>-admin` Service publishing oteldb's admin API (`8090`). Absent ⇒ no Service; the API is still served inside every storage pod. See [Admin API](#admin-api). |
| `resources`, `nodeSelector`, `affinity`, `tolerations`, `topologySpreadConstraints`, `podSecurityContext`, `securityContext`, `podAnnotations`, `podLabels`, `serviceAccountName` | Standard pod scheduling/security knobs. |
| `extraConfig` | Arbitrary raw oteldb config **deep-merged** over the generated config — for fields the CRD does not model (auth, prometheus tuning, …). Nested objects merge key by key (`storage.policy` does not wipe `storage.backend`); operator-owned paths are [reserved](#reserved-extraconfig-paths). |

> `spec.policy` maps onto oteldb's `storage.policy`. `downsample`, `precision` and `recompress`
> work against oteldb v0.48.0; `retention` and `limits` landed upstream **after** it. Older oteldb
> builds — including the operator's current default image — ignore unknown config keys silently,
> so on those the two newer blocks are accepted by the API server and have no effect. Pin a newer
> `spec.image` before relying on them.

### Erasure coding

`spec.policy.ec` is the one storage policy that is only reachable in this operator's topology. The
engine gates erasure coding on a **shared-nothing cluster** — cluster mode *and* a private per-node
backend — which is precisely what an `OtelDBCluster` deploys: etcd is required, and the default
`file` backend is one PVC per pod. A single-node oteldb cannot use it, and neither can a cluster on
a shared bucket, where the store owns durability and every part stays full-copy.

```yaml
spec:
  replicas: 6
  policy:
    ec:
      data: 4
      parity: 2
      after: 72h   # omit to erasure-code every part
```

Two consequences are worth knowing before turning it on.

**`data+parity` replaces the replication factor.** Under an EC policy the engine's replication-factor
lookup returns `data+parity` and ignores the configured RF — for the unflushed head as well as the
converted parts. Setting both would mean one of them is silently dropped, so the operator
**rejects** `spec.policy.ec` together with `spec.cluster.replicationFactor` rather than pick a
winner. It also rejects a scheme with more shards than `spec.replicas`, since the ring would
otherwise clamp the placement to the members it has.

**Rack safety needs zones.** A placement survives losing a whole failure domain only with at least
`ceil((data+parity)/parity)` distinct zones — 3 for `{4,2}`. Below that the engine converts anyway
and warns; spread the nodes with `spec.topologySpreadConstraints` to earn it.

Erasure coding also needs an oteldb carrying [oteldb/oteldb#1279](https://github.com/oteldb/oteldb/pull/1279);
older builds ignore the key silently.

### Reserved `extraConfig` paths

`extraConfig` is merged recursively, so it can add keys the CRD does not model:

```yaml
extraConfig:
  auth:
    tenant_header: X-Scope-OrgID   # merged in; keeps backend/dir/cluster
```

The paths the operator renders from the spec are **reserved**: an `extraConfig` that sets one is
rejected, and the CR goes `Degraded` with reason `InvalidSpec` naming the offending path and the
spec field to use instead.

| Reserved path | Use instead |
|---|---|
| `metrics_backend`, `traces_backend`, `logs_backend` | not configurable — signals are always served from the embedded storage engine |
| `profiles_backend` | `spec.signals.profiles` |
| `storage.backend` | `spec.storage.backend` |
| `storage.dir`, `storage.wal_dir` | `spec.storage.dir` |
| `storage.s3` | `spec.storage.s3` |
| `storage.cluster` (whole subtree) | `spec.cluster`, `spec.etcd.endpoints` |
| `storage.flush_interval`, `storage.read_cache_bytes`, `storage.decode_cache_bytes`, `storage.decode_memory_bytes`, `storage.aggregate_stats` | `spec.engine` |
| `storage.policy.retention`, `storage.policy.limits`, `storage.policy.downsample`, `storage.policy.precision`, `storage.policy.recompress`, `storage.policy.ec` | `spec.policy` |

`storage.policy` is now modelled in full, so the whole block is reserved.

`spec.ingest.extraConfig` is merged over the generated `odbingest.yml` under the same rules, with
the whole `cluster` block reserved for `spec.cluster` and `spec.etcd.endpoints`, and the whole
`tenant` block reserved for `spec.ingest.tenant`.

`spec.query.extraConfig` is merged over the generated `odbselect.yml`, reserving `cluster` and the
five listener addresses (`prometheus.bind`, `loki.bind`, `tempo.bind`, `pyroscope.bind`,
`health.bind`). The binds are reserved because odbselect keeps serving its other listeners if one
moves: the Service port the operator published would point at nothing while the pod stayed `Ready`.
Everything else in a block is free — `prometheus.max_samples`, `loki.max_sample_rows`, per-listener
`auth`, `shutdown_timeout`. Use `spec.signals` to turn an API off.

### Status

`status` reports `phase` (`Pending`/`Progressing`/`Ready`/`Degraded`), `replicas`,
`readyReplicas`, `ingestReplicas`, `ingestReadyReplicas`, `queryReplicas`, `queryReadyReplicas`, the
resolved `etcdEndpoints`, and standard `Available`/`Progressing`/`Degraded` conditions.
`kubectl get oteldbcluster` prints replicas, ready count, and phase. The phase tracks the storage
nodes: they hold the data, and a stateless pool still rolling out does not make the cluster
unavailable.

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
remote-write, `9090` PromQL, `3200` TraceQL (Tempo), `3100` LogQL (Loki), `4040` Pyroscope, `9464`
self-metrics, `13133` health. Peer replication: `7946` (headless Service).

The ingest Service (`<name>-ingest`) exposes `4317` OTLP gRPC, `4318` OTLP HTTP, `19291` Prometheus
remote-write and `9464` self-metrics. `odbingest` serves OTLP/HTTP, remote write and its health
endpoints on **one** listener (`19291`), so the Service publishes `4318` — the port stock OTLP/HTTP
exporters target — and remaps it onto that listener.

The query Service (`<name>-query`) exposes `9090` PromQL, `3200` TraceQL, `3100` LogQL, `4040`
Pyroscope and `9464` self-metrics, dropping any whose signal is disabled. `odbselect` serves each on
its own listener, so these are published one-to-one; its health listener (`13133`) is probed, not
published.

`8090` is oteldb's admin API bind, not a self-metrics endpoint. It is served in every storage pod
whether or not anything publishes it, and it is deliberately kept off the client Service — set
`spec.admin` to publish it on a Service of its own. See [Admin API](#admin-api).

### Self-metrics

Every pod exports its own metrics on `9464` in Prometheus format. The operator sets
`OTEL_METRICS_EXPORTER=prometheus` alongside `OTEL_EXPORTER_PROMETHEUS_HOST/PORT`, because
go-faster/sdk defaults that variable to `otlp` and only starts the `/metrics` server when it names
`prometheus` — without it the published port served nothing.

> **Behaviour change.** Self-metrics previously left the pods over OTLP only (to the SDK's default
> endpoint), and `9464` was published but dead. They are now scraped from `9464` instead. If you
> were collecting oteldb's self-metrics via OTLP, point a scrape at the `metrics` port on the client,
> ingest and query Services.

### Admin API

oteldb's admin API (`8090`) reports build info, health, runtime and storage statistics, and it can
trigger the engine's maintenance and compaction passes and the per-stream storage-cost attribution
report — documented upstream as *the heaviest call the storage engine exposes*, since it decodes
every accounted byte column of every live part.

`spec.admin` publishes it on a dedicated `<name>-admin` Service. It is **opt-in and absent by
default**:

```yaml
spec:
  admin:
    service:
      type: ClusterIP   # the default
```

Three notes:

- **It is a separate Service, not a port on the client Service.** An endpoint that can start a
  compaction should not ride the Service that PromQL and OTLP share, where an ingress or a broad
  NetworkPolicy picks it up by default. A separate Service also keeps exposure and reachability
  independent: admin can stay `ClusterIP` while the client Service is a `LoadBalancer`, and it
  carries its own annotations.
- **Enabling it does not roll the pods.** oteldb registers the admin server unconditionally
  (`cmd/oteldb/admin.go`), and its bind defaults to `:8090` whether or not the config declares the
  block — so the listener already exists. The Service targets the port by number and the pod
  template is untouched.
- **It has no auth of its own.** Treat the Service as privileged and restrict it with a
  NetworkPolicy, or add oteldb's auth via `spec.extraConfig`.

Before this, the admin port was *accidentally* reachable: it was published under a port named
`metrics`, and moving self-metrics to `9464` removed that exposure without anyone deciding it
should go. Neither state was a decision; `spec.admin` is.

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
- `ingest.go` / `query.go` — the stateless `odbingest` and `odbselect` pools: config, Service,
  Deployment.
- `admin.go` — the opt-in admin API Service.
- `naming.go` — names, labels, ports.

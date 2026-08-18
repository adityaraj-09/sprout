# Sprout architecture

Sprout is a **control plane** for copy-on-write Postgres branching. The server owns replicas, clones, and postmasters. Clients (CLI, npm SDK, `psql`) never touch PGDATA directly.

## 1. System context

```mermaid
flowchart LR
  subgraph clients [Clients]
    CLI["sprout CLI / sproutdb-cli"]
    SDK["SproutClient SDK"]
    PSQL["psql / app"]
  end

  subgraph dns [DNS]
    Wild["*.strido.fit A/AAAA → VM"]
  end

  subgraph vm [Sprout VM]
    API["sprout-server HTTP :8080"]
    PX["SNI proxy :5432"]
  end

  subgraph up [Upstreams]
    SUPA["Supabase / prod"]
    LAB["Lab primary :55431"]
  end

  CLI -->|Bearer token REST| API
  SDK -->|Bearer token REST| API
  PSQL -->|"postgresql://test-x.host:5432/postgres"| PX
  Wild --> PX
  API -->|physical or logical connect| SUPA
  API -->|physical or logical connect| LAB
```

Two planes:

| Plane | Port | Who talks | Purpose |
|-------|------|-----------|---------|
| Control | `:8080` | CLI / SDK | connect, branch, suspend, doctor |
| Data | `:5432` (domain) or unique ports (localhost / IP) | `psql` / apps | SQL against a replica or branch |

## 2. Inside `sprout-server`

One process. HTTP, the SNI proxy, and the reconciler share the same SQLite notebook and the same data root.

```mermaid
flowchart TB
  subgraph proc ["sprout-server process"]
    HTTP["api/ — HTTP + Bearer auth"]
    ORCH["branch/ — orchestrator"]
    REP["replica/ — basebackup, pub/sub, replay pause"]
    ST["storage/ — ZFS / APFS / copy"]
    CMP["compute/ — local pg_ctl or Docker"]
    PG["postgres/ — initdb, hba, SCRAM, PrepareClone"]
    PXP["pgproxy/ — SSLRequest + TLS SNI splice"]
    REC["reconcile/ — 30s compute vs metadata"]
    META["meta/ — SQLite control.db"]
  end

  HTTP --> ORCH
  ORCH --> REP
  ORCH --> ST
  ORCH --> CMP
  ORCH --> PG
  ORCH --> META
  PXP --> META
  REC --> META
  REC --> CMP
  REC --> ST
  CMP --> PG
```

Package map:

```text
cmd/sprout          thin HTTP client (CLI)
cmd/sprout-server   control plane + reconciler + SNI proxy

internal/
  api/        HTTP routes, Bearer auth
  branch/     init / connect / create / lifecycle / doctor
  replica/    pg_basebackup, standby, logical pub/sub
  storage/    APFS / ZFS CoW (copy fallback)
  compute/    local pg_ctl (Docker optional)
  postgres/   initdb, checkpoint, PrepareClone, advertised URLs
  pgproxy/    TLS SNI router on :5432
  mongoproxy/ TLS SNI passthrough on :27017
  meta/       SQLite → data/control.db
  reconcile/  keep compute vs metadata aligned
  config/     env defaults
```

## 3. Data plane on the VM

Each connector and each branch is a **separate database process** with its own data dir and loopback port. The proxies are the only public database listeners when `SPROUT_PUBLIC_HOST` is a DNS name.

```mermaid
flowchart TB
  subgraph public [Public NIC]
    API["HTTP :8080"]
    SNI["TLS SNI proxy :5432"]
    MNI["Mongo SNI passthrough :27017"]
  end

  subgraph loop [Loopback]
    RX["replica x  127.0.0.1:55434  trust"]
    RY["replica y  127.0.0.1:55435  trust"]
    BX["branch test from x  127.0.0.2:55440  SCRAM"]
    BY["branch test from y  127.0.0.2:55441  SCRAM"]
    MX["mongod atlas  127.0.0.1:55461  TLS"]
    MAIN["optional main  :55432"]
  end

  subgraph disk ["$SPROUT_DATA"]
    DB["control.db"]
    TLS["tls/server.crt"]
    D1["replicas/x"]
    D2["replicas/y"]
    D3["branches/test-x"]
    D4["branches/test-y"]
  end

  SNI -->|"SNI test-x.host"| BX
  SNI -->|"SNI test-y.host"| BY
  SNI -->|"SNI x.host"| RX
  MNI -->|"SNI atlas.host"| MX
  API --> DB
  SNI --> TLS
  MNI --> TLS
  RX --- D1
  RY --- D2
  BX --- D3
  BY --- D4
```

Hostname rule: connectors stay `<name>.host`; branches become `<branch>-<connector>.host` so two `test` branches do not collide.

```text
postgresql://sprout:<pass>@test-x.strido.fit:5432/postgres
postgresql://sprout:<pass>@test-y.strido.fit:5432/postgres
```

`/postgres` is the database inside that instance, not the branch name. Localhost and raw IPs skip the proxy and keep the unique port.

The proxy dials **127.0.0.2** (SCRAM) so it does not inherit stock **127.0.0.1 trust** used by the control plane.

## 4. Connect (named replica)

```mermaid
sequenceDiagram
  participant CLI as sprout CLI
  participant API as sprout-server
  participant ST as storage
  participant CMP as compute
  participant UP as upstream Postgres
  participant R as local replica PGDATA

  CLI->>API: POST /v1/projects/default/connect
  alt physical
    API->>UP: pg_basebackup -R
    UP-->>R: full cluster copy
    API->>CMP: pg_ctl start (hot standby)
  else logical
    API->>UP: CREATE PUBLICATION + slot
    API->>ST: EnsureVolume replicas/name
    API->>CMP: initdb + start writable primary
    API->>R: pg_dump schema + CREATE SUBSCRIPTION
  end
  API->>API: alloc port, write control.db
  API-->>CLI: connection_string
```

Physical replicas stay standbys (WAL replay). Logical replicas are writable primaries; branches still CoW that directory.

MongoDB connectors (`mongodb://` / `mongodb+srv://`) skip this Postgres path: `mongodump` into a local standalone `mongod`, then the same CoW snapshot/clone. There is no oplog follow. Clients use `mongodb://sprout:<pass>@<host>:27017/?tls=true` when the SNI passthrough is on (`SPROUT_MONGO_PROXY=false` keeps unique ports).

## 5. Branch create (CoW)

```mermaid
sequenceDiagram
  participant CLI as sprout CLI
  participant API as sprout-server
  participant P as parent replica or main
  participant ST as storage ZFS/APFS/copy
  participant B as branch PGDATA

  CLI->>API: POST /branches {"name":"test","from":"x"}
  API->>P: lag gate if standby
  API->>P: pause WAL replay + CHECKPOINT
  API->>ST: Snapshot parent PGDATA
  API->>P: resume replay
  API->>ST: Clone into branches/test-x
  API->>B: PrepareClone strip standby.signal
  API->>B: pg_ctl start independent primary
  API-->>CLI: postgresql://sprout:…@test-x.host:5432/postgres
```

On ZFS, each main / replica / branch is a **child dataset**. Snapshot and clone use those datasets, not the pool root.

## 6. Client SQL through SNI

```mermaid
sequenceDiagram
  participant App as psql / app
  participant PX as pgproxy :5432
  participant PG as Postgres 127.0.0.2:port

  App->>PX: TCP + SSLRequest
  PX-->>App: S
  App->>PX: TLS ClientHello SNI=test-x.strido.fit
  PX->>PX: Lookup control.db → port 55440
  PX->>PG: splice plaintext to 127.0.0.2:55440
  App->>PG: SCRAM + SQL  via the TLS tunnel
```

Disable with `SPROUT_PG_PROXY=false` to advertise unique Postgres ports again (no proxy).

MongoDB clients start TLS immediately (`tls=true`). `mongoproxy` peeks SNI from the ClientHello and splices TCP to loopback `mongod`, which terminates TLS.

```mermaid
sequenceDiagram
  participant App as mongosh / app
  participant PX as mongoproxy :27017
  participant M as mongod 127.0.0.1:port

  App->>PX: TLS ClientHello SNI=feat-atlas.strido.fit
  PX->>PX: Lookup control.db → port 55461
  PX->>M: splice ClientHello + TCP
  App->>M: TLS + SCRAM end-to-end
```

`SPROUT_MONGO_PROXY=false` advertises unique Mongo ports instead.

## 7. Storage and compute

```mermaid
flowchart LR
  subgraph storage [storage.Provider]
    ZFS["ZFS snapshot/clone"]
    APFS["APFS cp -c"]
    COPY["copy cp -a fallback"]
  end

  subgraph compute [compute.Provider]
    LOCAL["local pg_ctl"]
    DOCKER["docker postgres image"]
  end

  DET["Detect from env / OS"] --> storage
  DET --> compute
```

| Layer | Default pick |
|-------|----------------|
| Storage | `SPROUT_ZFS_DATASET` → ZFS; else APFS on macOS; else full copy |
| Compute | local `pg_ctl` so `initdb` and runtime share a major; Docker if `SPROUT_COMPUTE=docker` |

## 8. Reconciler

Every 30s, `internal/reconcile` walks `control.db`:

- Stuck `creating` / `resetting` / `deleting` → error or cleanup
- `active` with dead postmaster → `crashed` (or auto-resume if `SPROUT_AUTO_RESUME=true`)
- Same for connectors

It does not replace the API; it heals process state after a crash.

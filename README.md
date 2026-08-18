# sprout

Open-source **Postgres CoW branching** (plus MongoDB dump-restore connectors): near-instant database branches plus production sync via named connectors.

Spin up independent database instances that start as near-instant clones of a parent dataset (local demo or a replica of production), then diverge freely.

**VM / Azure from scratch:** see [`SETUP.md`](SETUP.md) (ZFS disk, Postgres 17 tools, firewall, connect + branch).  
**System diagrams:** [`ARCHITECTURE.md`](ARCHITECTURE.md).  
**LLM / agent skill:** [`SKILL.md`](SKILL.md) — how to drive the Sprout CLI against a hosted server.

---

## What it does

| Capability | How |
|------------|-----|
| **CoW branches** | Snapshot + clone PGDATA (APFS `cp -c` on macOS; ZFS on Linux) |
| **Control plane** | HTTP API + thin CLI; metadata in `data/control.db` |
| **Lifecycle** | create / list / get / reset / delete / suspend / resume |
| **Connectors** | Multiple named remotes; each gets its own local replica + port |
| **Physical sync** | `pg_basebackup` → hot standby → branch with replay pause |
| **Logical sync** | Publication + schema dump + subscription (e.g. Supabase) |
| **MongoDB (v1)** | `mongodump` snapshot into local `mongod`; CoW branches; `:27017` SNI passthrough; no oplog |

```text
upstream(s)  ──connect --name──►  data/replicas/<name>/
                                        │
                              branch create --from <name>
                                        ▼
                               data/branches/<branch>/   (independent primary)
```

`sprout init` still creates a local demo primary at `data/main` (port **55432**). Use `--from=main` to branch from it.

### Node client (npm)

```bash
npm install -g sproutdb-cli
# or from this repo:
make npm-link
```

Package: **[`sproutdb-cli`](https://www.npmjs.com/package/sproutdb-cli)** — installs the `sprout` binary + `SproutClient` SDK. Still needs `./bin/sprout-server` running.  
Docs: [`npm/README.md`](npm/README.md) · Repo: [github.com/adityaraj-09/sprout](https://github.com/adityaraj-09/sprout)

---

## Requirements

- **Go** 1.24+
- **Postgres client/server tools** on `PATH` (`initdb`, `pg_ctl`, `psql`, `pg_basebackup`, `pg_dump`)
  - For remote PG 17 (e.g. Supabase), prefer matching client bits:  
    `export PATH="/opt/homebrew/opt/postgresql@17/bin:$PATH"`
- **macOS**: APFS volume (CoW via `cp -c`)
- **Linux**: ZFS preferred when available; otherwise detection falls through to APFS-style paths only if appropriate
- Auth token for API (default `dev-token`)

---

## Quick start

```bash
export PATH="/opt/homebrew/bin:$PATH"   # or postgresql@17 bin as needed
make build
```

### Option A — local demo (no remote)

```bash
# terminal 1
./bin/sprout-server

# terminal 2
./bin/sprout init
./bin/sprout branch create alice --from=main
./bin/sprout branch list
psql postgresql://localhost:<port>/postgres
```

### Option B — lab “production” + named connector

```bash
make lab-primary          # fake prod on :55431 (wal_level=logical)

# terminal 1
./bin/sprout-server

# terminal 2
./bin/sprout connect --name=lab \
  "postgresql://$(whoami)@127.0.0.1:55431/postgres"
./bin/sprout status lab
./bin/sprout branch create from-lab --from=lab
psql postgresql://localhost:<port>/postgres -c 'SELECT count(*) FROM products;'
```

### Option C — multiple connectors

```bash
./bin/sprout connect --name=lab --mode=physical \
  "postgresql://$(whoami)@127.0.0.1:55431/postgres"

./bin/sprout connect --name=supabase --mode=logical \
  "postgresql://user:pass@db.xxx.supabase.co:5432/postgres"

./bin/sprout connector list
./bin/sprout branch create feat-a --from=lab
./bin/sprout branch create feat-b --from=supabase
```

If **one** connector exists, `--from` is optional. With **multiple**, `--from` is required.

---

## Team use (one hosted server)

One VM, one project (`default`). Anyone with a GitHub account can `sprout login`. GitHub users are isolated: each person only sees **their** connectors and **their** branches. Pre-GitHub / unowned rows stay visible to the machine `SPROUT_TOKEN` only.

**Server (once):** create a GitHub OAuth App, enable **Device Flow**, then:

```bash
export SPROUT_GITHUB_CLIENT_ID=Iv1.xxxxxxxx
# optional lock-down later:
# export SPROUT_GITHUB_USERS=alice,bob
# export SPROUT_GITHUB_ORGS=my-org
# keep a strong SPROUT_TOKEN as a machine/break-glass token
```

**Each person:**

```bash
sprout config set api-url http://strido.fit:8080
sprout login          # opens GitHub in the browser; saves ~/.sprout/config.json
sprout whoami
sprout connect --name=supabase --mode=logical 'postgresql://…'   # your replica
sprout branch create testdb --from=supabase
```

A later `sprout connect` to the **same** Supabase URL clones a local replica (no extra WAL sender). Only the first live replica of that database opens a logical slot on prod.

Use **your** URLs (GitHub login is in the hostname so alice and bob can both have `testdb` / `supabase`):

- connector: `postgresql://sprout:<pass>@supabase-alice.strido.fit:5432/postgres`
- branch: `postgresql://sprout:<pass>@testdb-alice-supabase.strido.fit:5432/postgres`

`sprout connector list` / `sprout branch list` only show your rows. `sprout logout` drops the GitHub token and does **not** fall back to the shared machine token. Shared `main` (`sprout init`) is machine-token only.

Keep `SPROUT_TOKEN` for scripts and ops (sees everything, including leftover unowned connectors).

---

## Architecture

Full diagrams (context, SNI routing, connect, CoW branch create, reconciler): [`ARCHITECTURE.md`](ARCHITECTURE.md).

```mermaid
flowchart TB
  subgraph clients [Clients]
    CLI["sprout CLI / SDK"]
    PSQL["psql / apps"]
  end

  subgraph vm ["sprout-server on the VM"]
    API["HTTP API :8080"]
    PX["TLS SNI proxy :5432"]
    ORCH["branch orchestrator"]
    META["SQLite control.db"]
    ST["storage ZFS / APFS / copy"]
    CMP["compute pg_ctl"]
  end

  subgraph data [PGDATA]
    RX["replicas/x :55434"]
    RY["replicas/y :55435"]
    BX["branches/test-x :55440"]
    BY["branches/test-y :55441"]
  end

  subgraph up [Upstreams]
    U1["prod / Supabase"]
    U2["lab primary"]
  end

  CLI -->|REST Bearer| API
  API --> ORCH
  ORCH --> META
  ORCH --> ST
  ORCH --> CMP
  ORCH --> RX
  ORCH --> RY
  ORCH --> BX
  ORCH --> BY
  PX -->|"SNI test-x.host"| BX
  PX -->|"SNI test-y.host"| BY
  PSQL --> PX
  RX -.->|physical or logical| U1
  RY -.->|physical or logical| U2
```

```text
cmd/sprout          thin HTTP client (CLI)
cmd/sprout-server   control plane + reconciler + SNI proxy

internal/
  api/        HTTP routes, Bearer auth
  branch/     orchestrate init / connect / create / lifecycle
  replica/    pg_basebackup, standby, logical pub/sub
  storage/    APFS / ZFS CoW provider
  compute/    local pg_ctl (Docker stub)
  postgres/   initdb, checkpoint, PrepareClone, seed
  pgproxy/    TLS SNI router on :5432
  meta/       SQLite → data/control.db (imports legacy control.json once)
  reconcile/  keep compute vs metadata aligned
  config/     env defaults
```

**Branch create (physical standby parent)**

1. Lag gate  
2. `pg_wal_replay_pause` + `CHECKPOINT`  
3. CoW snapshot → clone into `data/branches/<name>/`  
4. Resume replay on the replica  
5. `PrepareClone` (strip standby signals) → start as primary  

**Branch create (logical replica / local main)**

Same CoW path, but parent is a normal primary (checkpoint ± optional cold stop).
The clone inherits `pg_subscription`; Sprout detaches it so the branch is a
frozen snapshot and does not reuse the connector's prod replication slot.
Live changes keep flowing only on the **connector** (`sprout status <name>`).

---

## Data layout

```text
data/
  control.db                # SQLite control plane (projects, branches, connectors)
  control.json              # legacy; auto-imported into control.db on first open
  main/                     # optional local demo (sprout init) — :55432
  lab-primary/              # scripts/lab-primary.sh — :55431
  replicas/<connector>/     # one PGDATA + port per connector
  branches/<name>/          # branch PGDATA
  snapshots/<name>/         # CoW snapshot refs (APFS)
  logs/*.log
```

Default port allocator starts at **55433** (`next_port` in `control.db`).  
Connectors and branches each get an allocated port.

`data/` is gitignored — never commit it (URLs may contain passwords).

---

## CLI reference

| Command | Description |
|---------|-------------|
| `sprout init` | Ensure default project + local `main` + seed demo |
| `sprout connect [--name=id] [--engine=postgres\|mongodb] [--mode=physical\|logical] <url>` | Bootstrap named replica |
| `sprout status [name]` | Replication lag / logical sync for a connector |
| `sprout connector list` | List connectors (password redacted) |
| `sprout connector delete <name> [--force]` | Drop local replica + remote pub; `--force` also deletes child branches |
| `sprout health` | `GET /healthz` |
| `sprout login` | GitHub device flow; saves token to `~/.sprout/config.json` |
| `sprout logout` | Drop the saved GitHub token |
| `sprout whoami` | Identity the server accepted |
| `sprout branch create <name> [--from=<connector\|main>]` | CoW branch |
| `sprout branch list` | Branches + replicas + main |
| `sprout branch get <name> [--from]` | JSON record |
| `sprout branch reset <name> [--from]` | Re-clone from stored snapshot |
| `sprout branch delete <name> [--from]` | Stop + destroy |
| `sprout branch suspend <name> [--from]` | Stop compute (`idle`) |
| `sprout branch resume <name> [--from]` | Start again |
| `sprout connector suspend <name>` | Stop connector replica **and** all its branches |
| `sprout connector resume <name>` | Start connector + idle branches again |

Defaults:

- `--name=primary` if omitted on connect  
- `--engine` inferred from URL (`mongodb://` / `mongodb+srv://` → mongodb, else postgres)  
- `--mode=physical` if omitted for Postgres; MongoDB is always `logical` (dump snapshot) 

---

## HTTP API

Base URL: `http://127.0.0.1:8080`  
Auth: `Authorization: Bearer <token>` (GitHub user token from `sprout login`, or `SPROUT_TOKEN`). `/healthz` and `/v1/auth/github` are open.

| Method | Path | Body / notes |
|--------|------|----------------|
| `GET` | `/healthz` | `{ "status": "ok" }` |
| `GET` | `/v1/auth/github` | public: `{client_id, host, scope, ready}` for device flow |
| `GET` | `/v1/whoami` | `{kind, login, id}` |
| `POST` | `/v1/init` | create/start local main |
| `GET` | `/v1/projects` | list projects |
| `GET` | `/v1/connectors` | all connectors (URL passwords redacted) |
| `POST` | `/v1/projects/{project}/connect` | `{"url","engine","mode","name"}` |
| `DELETE` | `/v1/projects/{project}/connectors/{name}` | delete replica; `?force=true` also deletes child branches |
| `GET` | `/v1/projects/{project}/replication?name=` | lag (name optional if sole connector) |
| `GET` | `/v1/projects/{project}/connectors/{name}/replication` | lag for one connector |
| `POST` | `/v1/projects/{project}/branches` | `{"name","from"}` |
| `GET` | `/v1/projects/{project}/branches` | list |
| `GET` | `/v1/projects/{project}/branches/{name}` | get |
| `DELETE` | `/v1/projects/{project}/branches/{name}` | delete |
| `POST` | `.../branches/{name}/reset` | reset |
| `POST` | `.../branches/{name}/suspend` | suspend |
| `POST` | `.../branches/{name}/resume` | resume |
| `POST` | `.../connectors/{name}/suspend` | suspend connector + branches |
| `POST` | `.../connectors/{name}/resume` | resume connector + branches |

Use `project` = `default` (resolved by name) or a project UUID.

---

## Connect modes

| Mode | Command | Behavior |
|------|---------|----------|
| **physical** | `connect --name=x URL` | Postgres: `pg_basebackup -R` into `data/replicas/x/`, streaming hot standby |
| **logical** | `connect --name=x --mode=logical URL` | Postgres: publication → init local PGDATA → `pg_dump --schema-only` → `CREATE SUBSCRIPTION` |
| **mongodb** | `connect --name=x 'mongodb://…'` | `mongodump` → local standalone `mongod`. No oplog follow, no Docker Mongo. With a DNS host, URLs use `:27017` (SNI passthrough) |

Logical is for hosts that block physical replication (common on managed Postgres / Supabase). Publication/subscription names are scoped per connector (`sprout_pub_<name>`, `sprout_sub_<name>`). Logical publications are limited to `public` schema tables where applicable.

MongoDB connect is a **point-in-time snapshot**, not continuous replication. `--tables=` is a collection allowlist and requires a database in the URL. Branches CoW the local `dbPath` and start as independent standalones. With a DNS `SPROUT_PUBLIC_HOST`, connection strings use `:27017` and `tls=true` (SNI selects the instance). Localhost / IP still use the unique allocated port. `SPROUT_MONGO_PROXY=false` keeps unique ports.

**Physical** branch create can pause WAL replay for a consistent snapshot.  
**Logical** local datasets are writable primaries; branches still CoW that directory.

---

## Environment

### Server (`sprout-server`)

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPROUT_DATA` | `./data` | Data root |
| `SPROUT_LISTEN` | `127.0.0.1:8080` | API bind (`0.0.0.0:8080` to expose) |
| `SPROUT_TOKEN` | `dev-token` | Bearer token (machine / break-glass; humans should `sprout login`) |
| `SPROUT_GITHUB_CLIENT_ID` | unset | GitHub OAuth App client ID (enable Device Flow on the app) |
| `SPROUT_GITHUB_USERS` | unset | Optional GitHub logins; omit to allow **any** GitHub user |
| `SPROUT_GITHUB_ORGS` | unset | Optional orgs; omit to allow **any** GitHub user |
| `SPROUT_GITHUB_HOST` | `https://github.com` | GitHub or GitHub Enterprise |
| `SPROUT_GITHUB_API` | `https://api.github.com` | GitHub API base |
| `SPROUT_PUBLIC_HOST` | `localhost` | Hostname in branch connection strings |
| `SPROUT_BRANCH_SUBDOMAIN` | auto | `true`/`false`. Auto-on when public host is a DNS name: URLs become `<name>-<owner>-<connector>.<host>:5432` |
| `SPROUT_PG_PROXY` | auto | SNI proxy on `:5432` when subdomains are on. `false` advertises unique ports instead |
| `SPROUT_PG_PROXY_PORT` | `5432` | Public Postgres port for the SNI proxy |
| `SPROUT_MONGO_PROXY` | auto | SNI passthrough on `:27017` when subdomains are on. `false` advertises unique Mongo ports |
| `SPROUT_MONGO_PROXY_PORT` | `27017` | Public Mongo port for the SNI passthrough |
| `SPROUT_TLS_CERT` / `SPROUT_TLS_KEY` | auto | TLS cert for the proxy; otherwise a self-signed wildcard is written to `$SPROUT_DATA/tls` |
| `SPROUT_PG_LISTEN` | auto | Postgres `listen_addresses` (`*` when public host is set) |
| `SPROUT_SAFE` | unset | Set `true` to keep fsync on (recommended when exposing) |
| `SPROUT_TRUST_REMOTE` | unset | Set `true` to keep trust auth for remote TCP (lab only). Default remote auth is SCRAM-SHA-256 |
| `SPROUT_DB_PASSWORD` | random | Shared DB password for advertised roles; otherwise generated per instance |
| `SPROUT_AUTO_RESUME` | unset | Set `true` to restart crashed connectors/branches |
| `SPROUT_COMPUTE` | `auto` | Compute provider (`local` / `docker` / `auto`) |
| `SPROUT_COLD_SNAP` | `true` | Cold-stop parent for non-standby snapshots (`false` to skip) |

### CLI (`sprout`)

| Variable | Default |
|----------|---------|
| `SPROUT_SERVER` | `http://127.0.0.1:8080` (or `apiUrl` in `~/.sprout/config.json`) |
| `SPROUT_TOKEN` | overrides the token saved by `sprout login` |
| `SPROUT_CONFIG` | path to `config.json` |

### Lab

| Variable | Default |
|----------|---------|
| `LAB_PRIMARY_PORT` | `55431` |
| `SPROUT_DATA` | repo `data/` (also used by lab script) |

---

## Host like a normal Postgres server

Branches are regular Postgres instances. On a VPS:

```bash
export SPROUT_LISTEN=0.0.0.0:8080
export SPROUT_PUBLIC_HOST=strido.fit       # or a raw IP / db.example.com
export SPROUT_TOKEN=some-secret
export SPROUT_SAFE=true                    # keep durable writes when public
./bin/sprout-server
```

When `SPROUT_PUBLIC_HOST` is a DNS name, Sprout runs a TLS SNI proxy on **5432** (Postgres) and a TLS SNI passthrough on **27017** (Mongo). The hostname selects the process (`test-x` vs `test-y`); you do not put the unique backend port in the URL:

```text
postgresql://sprout:<pass>@testdb-lab.strido.fit:5432/postgres
mongodb://sprout:<pass>@feat-alice-atlas.strido.fit:27017/?tls=true&tlsAllowInvalidCertificates=true&authSource=admin
```

Point a wildcard record `*.strido.fit` (and `strido.fit`) at the VM. Open firewall **5432**, **27017**, and **8080** (API). Localhost and raw IPs stay as-is (`localhost:55440`) with no proxy. `/postgres` is the database inside the instance, not the branch name.

Clients use TLS so SNI is visible (`sslmode=require` or libpq's default `prefer`; Mongo `tls=true`). A self-signed `*.strido.fit` cert is created under `$SPROUT_DATA/tls` unless you set `SPROUT_TLS_CERT` / `SPROUT_TLS_KEY`. Binding `:5432` / `:27017` needs root or `setcap cap_net_bind_service=+ep ./bin/sprout-server`.

Then:

```bash
sprout config set api-url http://strido.fit:8080
sprout config set token some-secret
sprout branch create testdb --from=lab
# connection_string → postgresql://sprout:<pass>@testdb-lab.strido.fit:5432/postgres
psql "postgresql://sprout:<pass>@testdb-lab.strido.fit:5432/postgres"
```

Remote auth through the proxy is **SCRAM-SHA-256** (loopback `127.0.0.1` stays trust so the control plane can still connect). Connection strings include the generated password. `SPROUT_PG_PROXY=false` restores unique Postgres ports and skips the Postgres proxy. `SPROUT_MONGO_PROXY=false` does the same for Mongo. `SPROUT_TRUST_REMOTE=true` restores the old open-trust lab behavior when unique ports are public.

---

## Makefile

```bash
make build              # bin/sprout + bin/sprout-server
make test               # go test ./...
make server             # build + run server
make lab-primary        # start lab Postgres on :55431
make lab-primary-stop
make clean              # stop main / lab / replicas / branches
make reset-data         # clean + wipe main, replicas, branches, snapshots, control.db
```

---

## Typical ports

| Port | Role |
|------|------|
| `5432` | Public Postgres SNI proxy when `SPROUT_PUBLIC_HOST` is a DNS name |
| `27017` | Public Mongo SNI passthrough (same hostnames; `mongod` stays on loopback) |
| `55431` | Lab primary (`scripts/lab-primary.sh`) |
| `55432` | Local demo `main` (`sprout init`) |
| `55433+` | Internal connector/branch ports (loopback when the proxy is on) |

Exact ports for connectors/branches are stored in `data/control.db` and shown by `connector list` / `branch list`.

---

## Security notes

- Connector URLs can contain credentials and are stored in `data/control.db`. Keep `data/` out of git (already ignored).
- List APIs redact passwords in URLs; rotate any secret that was pasted into a shell history or chat.
- Default token `dev-token` is for local use only — set `SPROUT_TOKEN` if you expose the listen address.
- Remote Postgres uses SCRAM unless `SPROUT_TRUST_REMOTE=true`. `sprout doctor` fails if remote trust is on without `SPROUT_SAFE=true`.

---

## Status / limitations

- **macOS + Homebrew Postgres** is the primary tested path (APFS CoW).
- **ZFS** provider creates a child dataset per main/replica/branch (not a single `main` snapshot). Docker compute is stubbed.
- Supabase **physical** replication typically fails (`pg_hba` / replication privileges) — use **logical**.
- Metadata is SQLite (`data/control.db`, WAL); legacy `control.json` is imported once if present.
- Reconciler aligns compute with branch **and** connector state. Unexpected downtime is `crashed` (not user `idle`); set `SPROUT_AUTO_RESUME=true` to restart.

---

## License / intent

Experimental OSS for CoW Postgres branches plus production connectors. Local-first; not a hosted SaaS.

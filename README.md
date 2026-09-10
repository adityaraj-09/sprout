# Sprout

Copy-on-write database branches for Postgres, MongoDB, and Qdrant, with named connectors that sync from production.

Create isolated, writable databases in seconds from a local replica of prod. Branches never talk to production. One connector keeps a single replication slot; apply runs on a schedule or on demand.

[Setup](SETUP.md) · [Architecture](ARCHITECTURE.md) · [CLI skill](SKILL.md) · [OpenAPI](docs/openapi.yaml) · [llms.txt](docs/llms.txt) · [npm client](https://www.npmjs.com/package/sproutdb-cli) · [CI example](examples/github-action.yml)

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![npm](https://img.shields.io/npm/v/sproutdb-cli)](https://www.npmjs.com/package/sproutdb-cli)

---

## Overview

```text
production / lab
        │  sprout connect --name=<connector>
        ▼
data/replicas/<connector>/     local replica (one slot on prod)
        │  sprout branch create <name> --from=<connector>
        ▼
data/branches/<name>/          independent primary — CoW clone, no prod traffic
```

`sprout init` still provisions a local demo cluster at `data/main` (port **55432**). Branch it with `--from=main`.

| | |
|---|---|
| **Branches** | Filesystem snapshot + clone of PGDATA, Mongo `dbPath`, or Qdrant storage (ZFS on Linux, APFS `cp -c` on macOS, full copy as fallback) |
| **Connectors** | Named remotes. Each gets its own local replica, port, and metadata row |
| **Physical Postgres** | `pg_basebackup` → hot standby → branch with WAL replay paused for a consistent snapshot |
| **Logical Postgres** | Publication + schema dump + subscription (Supabase and other managed hosts). Slot kept; apply paused between syncs |
| **MongoDB** | Point-in-time `mongodump` into local `mongod`. No oplog follow. CoW branches from that snapshot |
| **Qdrant** | Collection snapshot (or point scroll fallback) into local `qdrant`. No continuous sync. CoW branches from that snapshot |
| **Control plane** | `sprout-server` HTTP API + thin CLI. State in SQLite (`data/control.db`) |

---

## Requirements

- **Go 1.24+**
- **Postgres tools** (`initdb`, `pg_ctl`, `psql`, `pg_basebackup`, `pg_dump`) — required only for Postgres connectors / `sprout init`. Match the upstream major (Supabase PG 17): `export PATH="/opt/homebrew/opt/postgresql@17/bin:$PATH"`
- **MongoDB tools** (`mongod`, `mongodump`, `mongorestore`, `mongosh`) — only for `--engine=mongodb`
- **`qdrant` binary** on `PATH` — only for `--engine=qdrant`
- A Mongo/Qdrant-only `sprout-server` starts **without** Postgres binaries
- **macOS:** APFS volume for CoW clones
- **Linux:** ZFS when `SPROUT_ZFS_DATASET` is set; otherwise full copy
- API token (`dev-token` locally; set `SPROUT_TOKEN` when the API is public)

---

## Install

```bash
git clone https://github.com/adityaraj-09/sprout.git
cd sprout
make build          # bin/sprout + bin/sprout-server
```

Two CLIs share `~/.sprout/config.json`. Use **one** on PATH:

| Who | Install | Config |
|---|---|---|
| This repo / Go | `make build` → `./bin/sprout` | `SPROUT_SERVER` / `SPROUT_TOKEN` or the same config file |
| Hosted humans | `npm install -g sproutdb-cli` | `sprout config set api-url …` then `sprout login` |

```bash
npm install -g sproutdb-cli
sprout config set api-url http://YOUR_HOST:8080
sprout login
sprout doctor
```

The npm package is **[`sproutdb-cli`](https://www.npmjs.com/package/sproutdb-cli)** (`sprout` on PATH + `SproutClient` SDK). From this repo: `make npm-link`. See [`npm/README.md`](npm/README.md).

A production-style VM (ZFS, firewall, Postgres 17) is documented in [`SETUP.md`](SETUP.md).

---

## Quick start

```bash
make build
./bin/sprout-server                 # terminal 1
./bin/sprout doctor                 # terminal 2 — binaries, storage, DNS
```

### Local demo (needs Postgres tools)

```bash
./bin/sprout init
./bin/sprout branch create alice --from=main
./bin/sprout branch switch alice --from=main
./bin/sprout env --write=.env.sprout
./bin/sprout url                    # connection string only
```

Scripts and agents should use `--print-url` / `--format json` instead of scraping human text:

```bash
DATABASE_URL="$(sprout branch create feat --from=lab --print-url)"
```

### Lab primary + connector

```bash
make lab-primary                    # fake prod on :55431 (wal_level=logical)
./bin/sprout-server

./bin/sprout connect --name=lab \
  "postgresql://$(whoami)@127.0.0.1:55431/postgres"
./bin/sprout status lab
./bin/sprout branch create from-lab --from=lab
```

### Several remotes

```bash
./bin/sprout connect --name=lab --mode=physical \
  "postgresql://$(whoami)@127.0.0.1:55431/postgres"

./bin/sprout connect --name=supabase --mode=logical \
  "postgresql://user:pass@db.xxx.supabase.co:5432/postgres"

./bin/sprout connector list
./bin/sprout branch create feat-a --from=lab
./bin/sprout branch create feat-b --from=supabase
```

`--from` is optional when exactly one connector exists. With more than one, it is required.

---

## Hosted teams

One VM, one project (`default`). Anyone with a GitHub account can `sprout login`. Each GitHub user sees only their connectors and branches. Unowned / pre-GitHub rows are visible to the machine `SPROUT_TOKEN` only.

**Server (once)** — GitHub OAuth App with Device Flow enabled:

```bash
export SPROUT_GITHUB_CLIENT_ID=Iv1.xxxxxxxx
# export SPROUT_GITHUB_USERS=alice,bob     # omit = any GitHub user
# export SPROUT_GITHUB_ORGS=my-org
# keep a strong SPROUT_TOKEN as break-glass
```

**Each person:**

```bash
sprout config set api-url http://YOUR_HOST:8080
sprout login
sprout whoami
sprout doctor
sprout connector preflight --mode=logical 'postgresql://…'
sprout connect --name=supabase --mode=logical 'postgresql://…'
sprout branch create testdb --from=supabase --print-url
```

A second `sprout connect` to the **same** host:port/database clones an existing local replica (no extra prod slot). Only the first live replica of that URL opens a logical slot on production.

Hostnames include the GitHub login so two people can both use `testdb` / `supabase`:

| Role | Example |
|------|---------|
| Connector | `postgresql://sprout:<pass>@supabase-alice.strido.fit:5432/postgres` |
| Branch | `postgresql://sprout:<pass>@testdb-alice-supabase.strido.fit:5432/postgres` |

`/postgres` is the database **inside** the instance, not the branch name.

`sprout logout` drops the GitHub token and does not fall back to the shared machine token. `sprout init` (shared `main`) is machine-token only.

---

## Connect modes

| Mode | When | What it does |
|------|------|----------------|
| **physical** | You control WAL / replication | `pg_basebackup -R` into `data/replicas/<name>/`, streaming hot standby |
| **logical** | Managed Postgres / Supabase | Publication on prod → local `initdb` → schema dump → subscription. **Slot stays; apply pauses** after the initial copy |
| **mongodb** | Atlas / `mongodb://` | `mongodump` → local `mongod`. Snapshot only — no oplog |
| **qdrant** | Qdrant Cloud / `qdrant://` / `:6333` | Collection snapshot → local `qdrant`. Snapshot only — no continuous sync |

Logical publications target `public` schema tables (or `--tables=`). Slot names are per connector (`sprout_pub_<name>`, `sprout_sub_<name>`).

After logical connect, queued WAL is applied by:

- **`sprout sync [name]`** — apply now, then pause again
- **`SPROUT_SYNC_INTERVAL`** — default `1h`; set `off` / `0` to disable the ticker

Branches CoW the replica directory and **detach** any cloned subscription so they cannot steal the connector’s slot.

Mongo `--tables=` is a collection allowlist and requires a database in the URL. With a DNS `SPROUT_PUBLIC_HOST`, Mongo URLs use port **27017** and `tls=true` (SNI selects the instance). `SPROUT_MONGO_PROXY=false` keeps unique ports.

Qdrant `--tables=` is a collection allowlist. Infer engine from `qdrant://` / `qdrants://`, `*.qdrant.io`, or port **6333** (or pass `--engine=qdrant` on a generic `http(s)://` URL). With a DNS host, Qdrant URLs use port **6333** and HTTPS (SNI selects the instance). `SPROUT_QDRANT_PROXY=false` keeps unique ports. `sprout sync` is unsupported — reconnect with `--wipe` to refresh.

`sprout connector preflight <url>` checks WAL, slots, grants, replica identity, and local tools **without** creating a slot. `--branch-sql=@anonymize.sql` on connect (or `sprout connector hook`) runs Postgres SQL on every new branch. Idle branches auto-suspend after `SPROUT_IDLE_SUSPEND` (default **15m**; `off` to disable).

CI: copy [`examples/github-action.yml`](examples/github-action.yml). Agents: fetch `$SPROUT_SERVER/llms.txt` and `$SPROUT_SERVER/openapi.yaml`.

---

## Public Postgres, Mongo, and Qdrant URLs

On a VPS, branches are ordinary database processes. The control plane is HTTP; SQL goes through an SNI proxy when the public host is a DNS name.

```bash
export SPROUT_LISTEN=0.0.0.0:8080
export SPROUT_PUBLIC_HOST=strido.fit
export SPROUT_TOKEN=some-secret
export SPROUT_SAFE=true
./bin/sprout-server
```

```text
postgresql://sprout:<pass>@testdb-lab.strido.fit:5432/postgres
mongodb://sprout:<pass>@feat-alice-atlas.strido.fit:27017/?tls=true&tlsAllowInvalidCertificates=true&authSource=admin
https://feat-alice-vectors.strido.fit:6333/?api-key=<pass>&tlsAllowInvalidCertificates=true
```

Point `*.strido.fit` (and the apex) at the VM. Open **8080** (API), **5432** (Postgres SNI), **27017** (Mongo SNI), and **6333** (Qdrant SNI). Localhost and raw IPs skip the proxy and use unique ports (`localhost:55440`).

Clients need TLS so SNI is visible (`sslmode=require` or libpq `prefer`; Mongo `tls=true`; Qdrant HTTPS). A self-signed `*.host` cert is written under `$SPROUT_DATA/tls` unless you set `SPROUT_TLS_CERT` / `SPROUT_TLS_KEY`. Binding 5432/27017/6333 needs root or `setcap cap_net_bind_service=+ep ./bin/sprout-server`.

Remote auth through the proxy is **SCRAM-SHA-256** for Postgres (loopback `127.0.0.1` stays trust for the control plane). `SPROUT_PG_PROXY=false` / `SPROUT_MONGO_PROXY=false` / `SPROUT_QDRANT_PROXY=false` advertise unique ports instead. `SPROUT_TRUST_REMOTE=true` is lab-only open trust.

```bash
sprout config set api-url http://strido.fit:8080
sprout config set token some-secret
sprout branch create testdb --from=lab
psql "postgresql://sprout:<pass>@testdb-lab.strido.fit:5432/postgres"
```

---

## Architecture

Full diagrams: [`ARCHITECTURE.md`](ARCHITECTURE.md).

```mermaid
flowchart TB
  subgraph clients [Clients]
    CLI["sprout CLI / SDK"]
    PSQL["psql / apps"]
  end

  subgraph vm ["sprout-server"]
    API["HTTP API :8080"]
    PX["TLS SNI proxy :5432"]
    ORCH["branch orchestrator"]
    META["SQLite control.db"]
    ST["storage ZFS / APFS / copy"]
    CMP["compute pg_ctl"]
  end

  subgraph data [Data dirs]
    RX["replicas/x"]
    BX["branches/test-x"]
  end

  subgraph up [Upstreams]
    U1["prod / Supabase"]
  end

  CLI -->|Bearer REST| API
  API --> ORCH
  ORCH --> META
  ORCH --> ST
  ORCH --> CMP
  PSQL --> PX
  PX -->|SNI hostname| BX
  RX -.->|physical or logical| U1
```

**Physical branch create:** lag gate → pause WAL replay → checkpoint → CoW snapshot/clone → resume replay → `PrepareClone` (strip `standby.signal`) → start as primary.

**Logical / main branch create:** checkpoint (± cold stop) → same CoW path → detach cloned subscriptions so the branch never uses the connector’s prod slot.

```text
cmd/sprout          HTTP CLI
cmd/sprout-server   control plane, reconciler, SNI proxies

internal/
  api/        HTTP + Bearer auth
  branch/     init, connect, create, sync, lifecycle
  replica/    basebackup, standby, logical pub/sub
  storage/    ZFS / APFS / copy
  compute/    local pg_ctl (Docker stub)
  postgres/   initdb, PrepareClone, advertised URLs
  pgproxy/    TLS SNI on :5432
  meta/       SQLite control.db
  reconcile/  compute vs metadata
  config/     env defaults
```

---

## Data layout

```text
data/
  control.db              SQLite control plane
  control.json            legacy; imported once if present
  main/                   optional demo (sprout init) — :55432
  lab-primary/            scripts/lab-primary.sh — :55431
  replicas/<connector>/   one PGDATA or dbPath per connector
  branches/<name>/        branch data directory
  snapshots/<name>/       APFS snapshot refs
  logs/*.log
```

Ports start at **55433**. In-use listeners are skipped so a leftover `mongod` cannot block the next Postgres instance. `data/` is gitignored — it can contain connection secrets.

---

## CLI

| Command | Purpose |
|---------|---------|
| `sprout init` | Local demo project + `main` |
| `sprout connect [--name=] [--engine=] [--mode=] [--wipe\|--no-wipe] [--dry-run] [--tables=] [--branch-sql=] <url>` | Bootstrap a named replica |
| `sprout status [name]` | Connector lag / logical status |
| `sprout sync [name]` | Apply queued logical WAL now, then pause (slot kept) |
| `sprout connector list \| delete [--force] \| suspend \| resume <name>` | Connector lifecycle |
| `sprout branch create <name> [--from=]` | CoW branch |
| `sprout branch list \| get \| diff \| reset \| delete \| suspend \| resume \| switch` | Branch lifecycle (`--from` if the name is shared) |
| `sprout login \| logout \| whoami` | GitHub device flow |
| `sprout org …` | Orgs and members |
| `sprout doctor \| health` | Diagnostics — run doctor first on a new host |
| `sprout preflight` / `sprout connector preflight` | Probe an upstream URL; creates nothing |
| `sprout connector hook <name> --sql=@file.sql` | Postgres SQL on every new branch |
| `sprout env [name] [--write=.env.sprout]` | `DATABASE_URL` / `MONGODB_URI` / `QDRANT_*` |
| `sprout url [name]` | Connection string only |
| `sprout branch switch <name>` | Remember current branch (`sprout env` / `sprout url`) |

Global flags: `--print-url`, `--format json`, `--quiet`. Default human output no longer dumps a JSON blob.

Defaults: `--name=primary`; `--engine` from URL scheme; Postgres `--mode=physical`; Mongo and Qdrant are always dump-snapshot logical. `connector delete --force` also deletes child branches.

---

## HTTP API

Base: `http://127.0.0.1:8080`. Header: `Authorization: Bearer <token>` (GitHub user token or `SPROUT_TOKEN`). Unauthenticated: `/healthz`, `/v1/auth/github`, `/llms.txt`, `/openapi.yaml`. Project path is usually `default`. Full spec: [`docs/openapi.yaml`](docs/openapi.yaml).

<details>
<summary>Route table</summary>

| Method | Path | Notes |
|--------|------|--------|
| `GET` | `/healthz` | `{ "status": "ok" }` |
| `GET` | `/llms.txt` | Agent-oriented CLI/API notes |
| `GET` | `/openapi.yaml` | OpenAPI spec |
| `GET` | `/v1/auth/github` | Device-flow metadata |
| `GET` | `/v1/whoami` | `{kind, login, id, org}` |
| `POST` | `/v1/init` | Local `main` (machine token) |
| `GET` | `/v1/connectors` | Passwords redacted |
| `POST` | `/v1/projects/{project}/preflight` | Probe URL; no slot |
| `POST` | `/v1/projects/{project}/connect` | `url`, `engine`, `mode`, `name`, `wipe`, `dry_run`, `tables`, `branch_sql` |
| `PATCH` | `/v1/projects/{project}/connectors/{name}` | `{ "branch_sql": "..." }` |
| `POST` | `/v1/projects/{project}/sync` | Optional `?name=` |
| `POST` | `/v1/projects/{project}/connectors/{name}/sync` | Apply logical WAL now |
| `DELETE` | `/v1/projects/{project}/connectors/{name}` | `?force=true` deletes child branches |
| `GET` | `/v1/projects/{project}/replication` | `?name=` if several connectors |
| `GET` | `/v1/projects/{project}/connectors/{name}/replication` | One connector |
| `POST` | `/v1/projects/{project}/connectors/{name}/suspend\|resume` | Replica + child branches |
| `POST` | `/v1/projects/{project}/branches` | `{"name","from"}` |
| `GET` | `/v1/projects/{project}/branches` | List |
| `GET` / `DELETE` | `/v1/projects/{project}/branches/{name}` | `?from=` if ambiguous |
| `GET` | `.../branches/{name}/diff` | vs parent |
| `POST` | `.../branches/{name}/reset\|suspend\|resume` | |

Long jobs (`connect`, `branch create`, `sync`) stream NDJSON when `Accept: application/x-ndjson` or `?progress=1`.

</details>

---

## Configuration

### Server

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPROUT_DATA` | `./data` | Data root |
| `SPROUT_LISTEN` | `127.0.0.1:8080` | API bind (`0.0.0.0:8080` to expose) |
| `SPROUT_TOKEN` | `dev-token` | Machine / break-glass Bearer token |
| `SPROUT_GITHUB_CLIENT_ID` | unset | OAuth App client ID (Device Flow) |
| `SPROUT_GITHUB_USERS` / `SPROUT_GITHUB_ORGS` | unset | Optional allowlists; omit = any GitHub user |
| `SPROUT_GITHUB_HOST` / `SPROUT_GITHUB_API` | github.com / api.github.com | GitHub or GHE |
| `SPROUT_PUBLIC_HOST` | `localhost` | Hostname in advertised URLs |
| `SPROUT_BRANCH_SUBDOMAIN` | auto | `true` when public host is a DNS name |
| `SPROUT_PG_PROXY` / `SPROUT_PG_PROXY_PORT` | auto / `5432` | Postgres SNI proxy |
| `SPROUT_MONGO_PROXY` / `SPROUT_MONGO_PROXY_PORT` | auto / `27017` | Mongo SNI passthrough |
| `SPROUT_TLS_CERT` / `SPROUT_TLS_KEY` | auto | Else self-signed wildcard under `$SPROUT_DATA/tls` |
| `SPROUT_PG_LISTEN` | auto | `listen_addresses` (`*` when public host is set) |
| `SPROUT_SAFE` | unset | `true` keeps fsync on when public |
| `SPROUT_TRUST_REMOTE` | unset | `true` = remote trust (lab only). Default remote auth is SCRAM |
| `SPROUT_DB_PASSWORD` | random | Shared advertised password; else per instance |
| `SPROUT_AUTO_RESUME` | unset | `true` restarts crashed connectors/branches |
| `SPROUT_SYNC_INTERVAL` | `1h` | Logical apply cadence. `off` / `0` disables ticker (`sprout sync` still works) |
| `SPROUT_IDLE_SUSPEND` | `15m` | Auto-stop idle branches. `off` / `0` disables |
| `SPROUT_COMPUTE` | `auto` | `local` / `docker` / `auto` |
| `SPROUT_COLD_SNAP` | `true` | Cold-stop parent for non-standby snapshots |

### CLI

| Variable | Default |
|----------|---------|
| `SPROUT_SERVER` | `http://127.0.0.1:8080` or `apiUrl` in `~/.sprout/config.json` |
| `SPROUT_TOKEN` | Overrides the token from `sprout login` |
| `SPROUT_ORG` | Current org (`sprout org use`) |
| `SPROUT_CONFIG` | Path to `config.json` |

Lab: `LAB_PRIMARY_PORT` defaults to `55431`.

---

## Makefile

```bash
make build              # bin/sprout + bin/sprout-server
make test               # go test ./...
make server             # build + run server
make lab-primary        # lab Postgres on :55431
make lab-primary-stop
make clean              # stop main / lab / replicas / branches
make reset-data         # clean + wipe data dirs and control.db
```

---

## Ports

| Port | Role |
|------|------|
| `5432` | Public Postgres SNI proxy (DNS `SPROUT_PUBLIC_HOST`) |
| `27017` | Public Mongo SNI passthrough |
| `55431` | Lab primary |
| `55432` | Local `main` |
| `55433+` | Internal connector and branch ports (loopback when the proxy is on) |

Exact allocations live in `control.db` and in `connector list` / `branch list`.

---

## Security

- Connector URLs (including passwords) are stored in `data/control.db`. Do not commit `data/`.
- List APIs redact URL passwords. Rotate anything pasted into a shell or chat.
- `dev-token` is local-only. Set `SPROUT_TOKEN` before exposing `:8080`.
- Remote Postgres is SCRAM unless `SPROUT_TRUST_REMOTE=true`. `sprout doctor` fails if remote trust is on without `SPROUT_SAFE=true`.

---

## Limitations

- Primary test path is **macOS + Homebrew Postgres** (APFS CoW).
- **ZFS** uses a child dataset per main/replica/branch. Docker compute is a stub.
- Supabase **physical** replication usually fails (`pg_hba` / privileges) — use **logical**.
- Unexpected compute loss is `crashed`, not user `idle`. Set `SPROUT_AUTO_RESUME=true` to restart.

---

## License

Experimental open source. Local-first control plane — not a hosted SaaS. The npm client is MIT; see [`npm/package.json`](npm/package.json).

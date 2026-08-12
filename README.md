# sprout

Open-source **Postgres CoW branching**: near-instant database branches plus production sync via named connectors.

Spin up independent Postgres instances that start as near-instant clones of a parent dataset (local demo or a replica of production), then diverge freely.

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

## Architecture

```text
cmd/sprout          thin HTTP client (CLI)
cmd/sprout-server   control plane + reconciler

internal/
  api/        HTTP routes, Bearer auth
  branch/     orchestrate init / connect / create / lifecycle
  replica/    pg_basebackup, standby, logical pub/sub
  storage/    APFS / ZFS CoW provider
  compute/    local pg_ctl (Docker stub)
  postgres/   initdb, checkpoint, PrepareClone, seed
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
| `sprout connect [--name=id] [--mode=physical\|logical] <url>` | Bootstrap named replica |
| `sprout status [name]` | Replication lag / logical sync for a connector |
| `sprout connector list` | List connectors (password redacted) |
| `sprout health` | `GET /healthz` |
| `sprout branch create <name> [--from=<connector\|main>]` | CoW branch |
| `sprout branch list` | Branches + replicas + main |
| `sprout branch get <name>` | JSON record |
| `sprout branch reset <name>` | Re-clone from stored snapshot |
| `sprout branch delete <name>` | Stop + destroy |
| `sprout branch suspend <name>` | Stop compute (`idle`) |
| `sprout branch resume <name>` | Start again |

Defaults:

- `--name=primary` if omitted on connect  
- `--mode=physical` if omitted  

---

## HTTP API

Base URL: `http://127.0.0.1:8080`  
Auth: `Authorization: Bearer <token>` (default `dev-token`; `/healthz` is open)

| Method | Path | Body / notes |
|--------|------|----------------|
| `GET` | `/healthz` | `{ "status": "ok" }` |
| `POST` | `/v1/init` | create/start local main |
| `GET` | `/v1/projects` | list projects |
| `GET` | `/v1/connectors` | all connectors (URL passwords redacted) |
| `POST` | `/v1/projects/{project}/connect` | `{"url","mode","name"}` |
| `GET` | `/v1/projects/{project}/replication?name=` | lag (name optional if sole connector) |
| `GET` | `/v1/projects/{project}/connectors/{name}/replication` | lag for one connector |
| `POST` | `/v1/projects/{project}/branches` | `{"name","from"}` |
| `GET` | `/v1/projects/{project}/branches` | list |
| `GET` | `/v1/projects/{project}/branches/{name}` | get |
| `DELETE` | `/v1/projects/{project}/branches/{name}` | delete |
| `POST` | `.../branches/{name}/reset` | reset |
| `POST` | `.../branches/{name}/suspend` | suspend |
| `POST` | `.../branches/{name}/resume` | resume |

Use `project` = `default` (resolved by name) or a project UUID.

---

## Connect modes

| Mode | Command | Behavior |
|------|---------|----------|
| **physical** | `connect --name=x URL` | `pg_basebackup -R` into `data/replicas/x/`, streaming hot standby |
| **logical** | `connect --name=x --mode=logical URL` | Create scoped publication → init local PGDATA → `pg_dump --schema-only` → `CREATE SUBSCRIPTION` with `copy_data` |

Logical is for hosts that block physical replication (common on managed Postgres / Supabase). Publication/subscription names are scoped per connector (`sprout_pub_<name>`, `sprout_sub_<name>`). Logical publications are limited to `public` schema tables where applicable.

**Physical** branch create can pause WAL replay for a consistent snapshot.  
**Logical** local datasets are writable primaries; branches still CoW that directory.

---

## Environment

### Server (`sprout-server`)

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPROUT_DATA` | `./data` | Data root |
| `SPROUT_LISTEN` | `127.0.0.1:8080` | API bind (`0.0.0.0:8080` to expose) |
| `SPROUT_TOKEN` | `dev-token` | Bearer token |
| `SPROUT_PUBLIC_HOST` | `localhost` | Hostname in branch connection strings |
| `SPROUT_PG_LISTEN` | auto | Postgres `listen_addresses` (`*` when public host is set) |
| `SPROUT_SAFE` | unset | Set `true` to keep fsync on (recommended when exposing) |
| `SPROUT_COMPUTE` | `auto` | Compute provider (`local` / `docker` / `auto`) |
| `SPROUT_COLD_SNAP` | `true` | Cold-stop parent for non-standby snapshots (`false` to skip) |

### CLI (`sprout`)

| Variable | Default |
|----------|---------|
| `SPROUT_SERVER` | `http://127.0.0.1:8080` |
| `SPROUT_TOKEN` | `dev-token` |

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
export SPROUT_PUBLIC_HOST=db.example.com   # or your server IP
export SPROUT_TOKEN=some-secret
export SPROUT_SAFE=true                    # keep durable writes when public
./bin/sprout-server
```

Then:

```bash
sprout config set api-url http://db.example.com:8080
sprout config set token some-secret
sprout branch create feat --from=lab
# connection_string → postgresql://db.example.com:55440/postgres
psql postgresql://db.example.com:55440/postgres
```

Open firewall ports for the API and each branch port you use (or put a reverse proxy / VPN in front).  
Auth is still **trust** over TCP when remote listen is on — fine for a locked-down VPC; not for the open internet without real passwords/TLS.

---

## Makefile

```bash
make build              # bin/sprout + bin/sprout-server
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
| `55431` | Lab primary (`scripts/lab-primary.sh`) |
| `55432` | Local demo `main` (`sprout init`) |
| `55433+` | Allocated for connectors and branches |

Exact ports for connectors/branches are stored in `data/control.db` and shown by `connector list` / `branch list`.

---

## Security notes

- Connector URLs can contain credentials and are stored in `data/control.db`. Keep `data/` out of git (already ignored).
- List APIs redact passwords in URLs; rotate any secret that was pasted into a shell history or chat.
- Default token `dev-token` is for local use only — set `SPROUT_TOKEN` if you expose the listen address.

---

## Status / limitations

- **macOS + Homebrew Postgres** is the primary tested path (APFS CoW).
- **ZFS** provider exists for Linux; Docker compute is stubbed.
- Supabase **physical** replication typically fails (`pg_hba` / replication privileges) — use **logical**.
- Metadata is SQLite (`data/control.db`, WAL); legacy `control.json` is imported once if present.
- Reconciler runs periodically to align compute with stored branch state.

---

## License / intent

Experimental OSS for CoW Postgres branches plus production connectors. Local-first; not a hosted SaaS.

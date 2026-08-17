---
name: sprout-cli
description: >
  Use the Sprout CLI to connect production Postgres or MongoDB, create copy-on-write database
  branches, and get psql/mongosh URLs. Trigger when the user mentions Sprout, sprout CLI,
  sproutdb-cli, database branches, connectors, or wants an isolated Postgres for
  testing against a hosted sprout-server.
---

# Sprout CLI

Sprout is a **control plane** for CoW Postgres branches. The CLI talks HTTP to `sprout-server`. It does not start Postgres itself.

Two clients exist:

| Client | Config | Notes |
|--------|--------|--------|
| Go `./bin/sprout` (this repo) | `~/.sprout/config.json` + `SPROUT_SERVER` / `SPROUT_TOKEN` | `sprout login` writes the same file as npm |
| npm `sproutdb-cli` | `sprout config set` / `sprout login` → `~/.sprout/config.json` | Same commands + `config` |

Prefer whichever `sprout` is on PATH. For a hosted VM, point the CLI at that server; do not start a second server.

## Point the CLI at the server

```bash
sprout config set api-url http://strido.fit:8080
sprout login          # GitHub device flow (opens a browser)
sprout whoami
sprout health
sprout doctor
```

Go binary without login file:

```bash
export SPROUT_SERVER=http://strido.fit:8080
export SPROUT_TOKEN=<machine token>
sprout health
```

One-shot (npm): `sprout --api-url=http://strido.fit:8080 --token=secret health`

Auth is `Authorization: Bearer <token>`. Humans use a **GitHub user token** from `sprout login` and only see their own connectors/branches. `/healthz` and `GET /v1/auth/github` are unauthenticated. The machine `SPROUT_TOKEN` still works for scripts and sees everything. Server must set `SPROUT_GITHUB_CLIENT_ID`. Any GitHub user can sign in unless `SPROUT_GITHUB_USERS` / `SPROUT_GITHUB_ORGS` is set.

## What to do (typical)

### Hosted server (per-user)

1. Each GitHub user runs **their own** `sprout connect` (own replica). There is no shared connector or shared `main`.
2. Confirm only **your** rows:

```bash
sprout connector list
sprout status supabase
```

3. Create a branch from **your** connector (lowercase `[a-z0-9-]`, not `main`):

```bash
sprout branch create testdb --from=supabase
```

Prints `connection_string` and a `psql` one-liner. Hostnames include your GitHub login (`testdb-alice-supabase.strido.fit`) so two people can both use `testdb`. Use **that URL** in the app.

4. `sprout logout` does not fall back to `SPROUT_TOKEN` / `dev-token` against a remote API. Re-login to keep working. `sprout init` is machine-token only.

### First-time / empty server

Logical (Supabase / managed Postgres):

```bash
sprout connect --name=supabase --mode=logical --dry-run 'postgresql://USER:PASS@HOST:5432/postgres'
sprout connect --name=supabase --mode=logical 'postgresql://USER:PASS@HOST:5432/postgres'
```

A **second** `sprout connect` to the same host:port/database clones a local replica (no extra Supabase WAL sender). Only the first live replica of that URL opens a logical slot on prod. `max_wal_senders` errors mean too many live slots — delete unused connectors, then connect again.

Physical (you control WAL / replication):

```bash
sprout connect --name=lab --mode=physical 'postgresql://user@127.0.0.1:55431/postgres'
```

MongoDB (dump snapshot into local `mongod`; no oplog, no `:27017` proxy):

```bash
sprout connect --name=atlas 'mongodb+srv://USER:PASS@cluster.mongodb.net/app'
sprout branch create feat --from=atlas
# mongosh "mongodb://sprout:<pass>@feat-<owner>-atlas.host:<port>/?tls=true&tlsAllowInvalidCertificates=true&authSource=admin"
```

`--engine` is inferred from the URL. MongoDB only supports `--mode=logical`. `--tables=orders,items` is a collection allowlist and requires a database in the URL. Connection strings always use the unique allocated port with `tls=true`.

Local demo only (no remote):

```bash
sprout init
sprout branch create alice --from=main
```

`connect` defaults: `--name=primary`, `--wipe` (destroys the local replica and rebootstrap). `--mode` defaults to `physical` for Postgres and `logical` for MongoDB. Use `--no-wipe` to resume. `--tables=a,b` allowlists logical Postgres tables or Mongo collections.

## Connection URLs

- `/postgres` is the **database name inside the instance**, not the branch name.
- With a DNS `SPROUT_PUBLIC_HOST` (e.g. `strido.fit`), URLs are:
  - connector: `postgresql://sprout:<pass>@supabase-alice.strido.fit:5432/postgres`
  - branch: `postgresql://sprout:<pass>@testdb-alice-supabase.strido.fit:5432/postgres`
  - unowned/machine: `postgresql://sprout:<pass>@<branch>-<connector>.strido.fit:5432/postgres`
- Port **5432** is the SNI proxy. Hostname selects the instance. Clients need TLS (`sslmode=require` or libpq `prefer`). Self-signed cert is normal; `verify-full` may fail.
- Localhost / raw IP: unique ports, no subdomain (`localhost:55440`).
- MongoDB: same hostname labels, but the URL is `mongodb://sprout:<pass>@<host>:<instance-port>/?tls=true&tlsAllowInvalidCertificates=true&authSource=admin`. There is **no** port-27017 proxy.
- A branch is an **independent primary**. It does not keep replicating from prod. The **connector replica** does. `sprout branch create` detaches any cloned logical subscription so the branch cannot steal the connector's WAL slot.
- Do **not** `sprout connect` using a branch URL as the upstream unless the user explicitly wants a replica-of-a-branch. Day-to-day testing = `psql` / app DSN to the branch URL.

## Commands

```text
sprout doctor
sprout health
sprout login
sprout logout
sprout whoami
sprout init
sprout connect [--name=<id>] [--engine=postgres|mongodb] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
sprout status [connector-name]
sprout connector list
sprout connector delete <name> [--force]
sprout connector suspend <name>
sprout connector resume <name>
sprout branch create <name> [--from=<connector|main>]
sprout branch list
sprout branch get|diff|reset|delete|suspend|resume <name> [--from=<connector>]
```

`--from` on get/delete/reset/suspend/resume/diff is required when two branches share the same name.

`connector delete` is blocked while child branches exist unless `--force` (also deletes those branches).

npm-only:

```text
sprout config set api-url|token|project <value>
sprout config get
sprout config path
sprout config unset api-url|token|project
```

## After create

```bash
sprout branch list
sprout branch get ar-login --from=supabase
psql "postgresql://sprout:<pass>@ar-login-supabase.strido.fit:5432/postgres"
```

Writes on a branch stay on that branch. `branch reset` re-clones from the snapshot taken at create (loses later writes). `suspend` / `resume` stop/start compute; data is kept.

## Errors LLMs should not “fix” by reconnecting

| Symptom | Do |
|---------|-----|
| `unauthorized` | Token mismatch — run `sprout login` or check `SPROUT_TOKEN` |
| `forbidden` / `github_user_not_allowed` | Allowlist is on and this GitHub user is not listed |
| `ambiguous_branch` | Pass `--from=<connector>` |
| `branch_exists` | Pick another name |
| `connector_has_branches` | Delete/suspend children or `--force` |
| `multiple connectors — pass --from` | Add `--from=` |
| `source_not_ready` | `connector resume` / `status` |
| `version_mismatch` | Postgres client major must match upstream |
| Server unreachable | `sprout-server` not running; do not start a local one if the user has a hosted VM |

Never commit connection strings or tokens. Never dump `~/.sprout/config.json` into the repo.

## HTTP (if shelling without CLI)

Base: `SPROUT_SERVER`. Header: `Authorization: Bearer <token>`. Project path is `default`.

- `GET /healthz`
- `GET /v1/auth/github` (public; client_id + host for device flow)
- `GET /v1/whoami`
- `GET /v1/doctor`
- `POST /v1/init`
- `GET /v1/connectors`
- `POST /v1/projects/default/connect` body `{"url","name","engine","mode","wipe","dry_run","tables"}`
- `GET /v1/projects/default/replication` and `/v1/projects/default/connectors/{name}/replication`
- `DELETE /v1/projects/default/connectors/{name}?force=true`
- `POST /v1/projects/default/connectors/{name}/suspend|resume`
- `POST /v1/projects/default/branches` body `{"name","from"}`
- `GET /v1/projects/default/branches`
- `GET|DELETE /v1/projects/default/branches/{name}?from=`
- `GET /v1/projects/default/branches/{name}/diff?from=`
- `POST /v1/projects/default/branches/{name}/reset|suspend|resume?from=`

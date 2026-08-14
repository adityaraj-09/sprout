---
name: sprout-cli
description: >
  Use the Sprout CLI to connect production Postgres, create copy-on-write database
  branches, and get psql URLs. Trigger when the user mentions Sprout, sprout CLI,
  sproutdb-cli, database branches, connectors, or wants an isolated Postgres for
  testing against a hosted sprout-server.
---

# Sprout CLI

Sprout is a **control plane** for CoW Postgres branches. The CLI talks HTTP to `sprout-server`. It does not start Postgres itself.

Two clients exist:

| Client | Config | Notes |
|--------|--------|--------|
| Go `./bin/sprout` (this repo) | `SPROUT_SERVER`, `SPROUT_TOKEN` | No `sprout config` command |
| npm `sproutdb-cli` | `sprout config set` → `~/.sprout/config.json` | Same commands + `config` |

Prefer whichever `sprout` is on PATH. For a hosted VM, point the CLI at that server; do not start a second server.

## Point the CLI at the server

npm (persists):

```bash
sprout config set api-url http://strido.fit:8080
sprout config set token <SPROUT_TOKEN>
sprout health
sprout doctor
```

Go binary / env:

```bash
export SPROUT_SERVER=http://strido.fit:8080
export SPROUT_TOKEN=<SPROUT_TOKEN>
sprout health
```

One-shot (npm): `sprout --api-url=http://strido.fit:8080 --token=secret health`

Auth is `Authorization: Bearer <token>`. One shared token for the whole server (no per-user accounts). `/healthz` is unauthenticated.

## What to do (typical)

### Shared hosted server (team)

1. **Do not** run `sprout connect` for every developer. That copies the whole database again.
2. One connector already exists (e.g. `supabase`). Confirm:

```bash
sprout connector list
sprout status supabase
```

3. Create a **personal branch** (lowercase `[a-z0-9-]`, not `main`):

```bash
sprout branch create ar-login --from=supabase
```

Prints `connection_string` and a `psql` one-liner. Use **that URL** in the app. Do not invent hosts or ports.

4. Name branches `initials-ticket` (`ar-login`, `priya-42`) so they do not collide. Same name + same connector = error. Same name from two connectors is allowed (`testdb --from=lab` vs `testdb --from=supabase`).

### First-time / empty server

Logical (Supabase / managed Postgres):

```bash
sprout connect --name=supabase --mode=logical --dry-run 'postgresql://USER:PASS@HOST:5432/postgres'
sprout connect --name=supabase --mode=logical 'postgresql://USER:PASS@HOST:5432/postgres'
```

Physical (you control WAL / replication):

```bash
sprout connect --name=lab --mode=physical 'postgresql://user@127.0.0.1:55431/postgres'
```

Local demo only (no remote):

```bash
sprout init
sprout branch create alice --from=main
```

`connect` defaults: `--name=primary`, `--mode=physical`, `--wipe` (destroys the local replica and rebootstrap). Use `--no-wipe` to resume. `--tables=a,b` allowlists logical tables.

## Connection URLs

- `/postgres` is the **database name inside the instance**, not the branch name.
- With a DNS `SPROUT_PUBLIC_HOST` (e.g. `strido.fit`), URLs are:
  - connector: `postgresql://sprout:<pass>@supabase.strido.fit:5432/postgres`
  - branch: `postgresql://sprout:<pass>@<branch>-<connector>.strido.fit:5432/postgres`
- Port **5432** is the SNI proxy. Hostname selects the instance. Clients need TLS (`sslmode=require` or libpq `prefer`). Self-signed cert is normal; `verify-full` may fail.
- Localhost / raw IP: unique ports, no subdomain (`localhost:55440`).
- A branch is an **independent primary**. It does not keep replicating from prod. The **connector replica** does.
- Do **not** `sprout connect` using a branch URL as the upstream unless the user explicitly wants a replica-of-a-branch. Day-to-day testing = `psql` / app DSN to the branch URL.

## Commands

```text
sprout doctor
sprout health
sprout init
sprout connect [--name=<id>] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <postgresql-url>
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
| `unauthorized` | Token mismatch with server `SPROUT_TOKEN` |
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
- `GET /v1/doctor`
- `POST /v1/init`
- `GET /v1/connectors`
- `POST /v1/projects/default/connect` body `{"url","name","mode","wipe","dry_run","tables"}`
- `GET /v1/projects/default/replication` and `/v1/projects/default/connectors/{name}/replication`
- `DELETE /v1/projects/default/connectors/{name}?force=true`
- `POST /v1/projects/default/connectors/{name}/suspend|resume`
- `POST /v1/projects/default/branches` body `{"name","from"}`
- `GET /v1/projects/default/branches`
- `GET|DELETE /v1/projects/default/branches/{name}?from=`
- `GET /v1/projects/default/branches/{name}/diff?from=`
- `POST /v1/projects/default/branches/{name}/reset|suspend|resume?from=`

# Phase 2 — control plane

Phase 1 proved CoW branching. Phase 2 wraps it in a server.

## Architecture

```text
ardent (CLI)  --HTTP-->  ardent-server
                           ├── control.json metadata (Store interface)
                           ├── storage (APFS/ZFS)
                           ├── compute (local pg_ctl | docker)
                           └── reconciler
```

> Metadata uses a locked JSON file for now (`control.json`) so Phase 2 builds
> cleanly on Go 1.24 without the SQLite toolchain bump. The `meta.Store`
> interface is the real design — swap in SQLite later without touching the API.
## Run

```bash
export PATH="/opt/homebrew/bin:$PATH"

# terminal 1
make build
./bin/ardent-server

# terminal 2
./bin/ardent init
./bin/ardent branch create feature-x
./bin/ardent branch list
./bin/ardent branch suspend feature-x
./bin/ardent branch resume feature-x
./bin/ardent branch reset feature-x
./bin/ardent branch delete feature-x
```

## Env

| var | default | meaning |
|-----|---------|---------|
| `ARDENT_DATA` | `./data` | PGDATA + control.db root |
| `ARDENT_LISTEN` | `127.0.0.1:8080` | server bind |
| `ARDENT_TOKEN` | `dev-token` | Bearer token |
| `ARDENT_COMPUTE` | `auto` | `local` \| `docker` \| `auto` |
| `ARDENT_COLD_SNAP` | `true` | stop main during snapshot |
| `ARDENT_SERVER` | `http://127.0.0.1:8080` | CLI target |

## API

```http
POST /v1/init
GET  /v1/projects
POST /v1/projects/default/branches   {"name":"x"}
GET  /v1/projects/default/branches
POST /v1/projects/default/branches/x/reset
POST /v1/projects/default/branches/x/suspend
POST /v1/projects/default/branches/x/resume
DELETE /v1/projects/default/branches/x
```

Auth: `Authorization: Bearer dev-token`

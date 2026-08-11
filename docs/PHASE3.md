# Phase 3 — production connector (full bootstrap)

`main` becomes a **physical streaming replica** of an upstream Postgres.

```text
lab-primary / real prod
        │  pg_basebackup (FULL COPY — slow once)
        │  + streaming WAL
        ▼
   data/main  (hot standby)
        │  pause replay → snapshot → clone
        ▼
     branches (independent primaries)
```

## Lab on this machine

```bash
# terminal A
./scripts/lab-primary.sh
./bin/ardent-server

# terminal B
./bin/ardent connect "postgresql://$(whoami)@127.0.0.1:55431/postgres"
./bin/ardent status
./bin/ardent branch create from-prod
psql postgresql://localhost:55433/postgres -c 'SELECT count(*) FROM products;'
```

## Modes

| Mode | Command | What it does |
|------|---------|--------------|
| physical | `ardent connect URL` | `pg_basebackup` → hot standby (full PGDATA twin) |
| logical | `ardent connect --mode=logical URL` | schema dump + `PUBLICATION`/`SUBSCRIPTION` (table sync) |

Logical is for hosts that block physical replication (e.g. Supabase). Local `main` is a normal primary; branches still use CoW on that local PGDATA.

```bash
./bin/ardent connect --mode=logical "postgresql://user:pass@host:5432/postgres"
./bin/ardent status
./bin/ardent branch create from-logical
```

## API

```http
GET  /v1/connectors
POST /v1/projects/default/connect
{"url":"postgresql://user@host:5432/postgres","mode":"logical"}

GET  /v1/projects/default/replication
```

CLI:

```bash
./bin/ardent connector list
./bin/ardent status
```

Physical connect runs `pg_basebackup -R -X stream` into `data/main`, starts standby on `:55432`, waits until lag is small.

Logical connect: create publication on primary → init local main → `pg_dump --schema-only` → `CREATE SUBSCRIPTION ... copy_data` → wait until tables are ready.

Branch create against a standby:
1. lag gate
2. `pg_wal_replay_pause` + `CHECKPOINT`
3. CoW snapshot/clone
4. resume replay
5. `PrepareClone` strips `standby.signal` so the branch is a primary

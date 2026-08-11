# ardent-clone

Open-source Ardent-style Postgres branching.

## Phases

| Phase | What |
|-------|------|
| 1 | CoW local branches (APFS/ZFS) |
| 2 | Control plane API + suspend/resume |
| 3 | Full bootstrap from prod (`pg_basebackup`) + streaming replica |

## Quick start (Phase 3 lab)

```bash
export PATH="/opt/homebrew/bin:$PATH"
make build
make lab-primary          # fake production on :55431

# terminal 1
./bin/ardent-server

# terminal 2
./bin/ardent connect "postgresql://$(whoami)@127.0.0.1:55431/postgres"
./bin/ardent status
./bin/ardent branch create from-prod
```

Docs: `docs/PHASE1.md`, `docs/PHASE2.md`, `docs/PHASE3.md`.

# sproutdb-cli

Node.js **CLI + SDK** for [Sprout](https://github.com/adityaraj-09/sprout) — talks to `sprout-server` over HTTP so you can connect production **Postgres** or **MongoDB**, branch databases, and copy ready-to-use connection strings.

Installs the **`sprout`** binary on your PATH.

**Version:** 0.7.0 — MongoDB connectors, orgs/members, streamed connect/branch progress, richer `sprout doctor`.

**Repo:** [https://github.com/adityaraj-09/sprout](https://github.com/adityaraj-09/sprout)  
**npm:** [https://www.npmjs.com/package/sproutdb-cli](https://www.npmjs.com/package/sproutdb-cli)  
**Agent skill:** [`SKILL.md`](../SKILL.md) in the Sprout repo (commands, hosted URLs, `--from=`).

## Requirements

- Node.js **18+**
- A running **`sprout-server`** (Go control plane from this repo)

```bash
git clone https://github.com/adityaraj-09/sprout.git
cd sprout && make build && ./bin/sprout-server
```

## Install

```bash
npm install -g sproutdb-cli
# or one-shot:
npx sproutdb-cli health
```

From this repo (dev):

```bash
cd npm && npm install && npm link
```

## Quick start

Point the CLI at your server once (saved to `~/.sprout/config.json`, mode `0600`):

```bash
sprout config set api-url http://127.0.0.1:8080
sprout login                              # GitHub device flow (hosted servers)
# or a machine token:
sprout config set token dev-token         # match SPROUT_TOKEN on a local server
sprout config get                         # token values are redacted
```

GitHub login creates a personal org named **`default`** (you are owner). Use `sprout org use` to switch.

Then:

```bash
sprout doctor
sprout health
sprout whoami                             # prints current org + memberships

# Postgres (logical, e.g. Supabase) — wipe/rebootstrap by default
sprout connect --name=prod --mode=logical --dry-run 'postgresql://...'
sprout connect --name=prod --mode=logical 'postgresql://...'

# MongoDB — dump snapshot (mongodump → mongorestore). Infers engine from the URL.
sprout connect --name=atlas --engine=mongodb 'mongodb+srv://USER:PASS@cluster.mongodb.net/app'
# or mongodb://...  (--engine=mongodb is optional when the URL scheme is mongodb)

sprout status prod
sprout status atlas

# branch from a connector
sprout branch create my-feature --from=prod
# prints:
#   postgresql://sprout@<name>-<github>-<connector>.host:5432/postgres
#   psql "postgresql://..."

sprout branch create mongo-feat --from=atlas
# prints:
#   mongodb://sprout@<name>-<github>-<connector>.host:27017/?tls=true
#   mongosh "mongodb://..."

sprout branch list
sprout branch get my-feature --from=prod
sprout branch diff my-feature --from=prod
sprout branch delete my-feature --from=prod

sprout connector list
sprout connector delete prod
sprout connector suspend prod   # stop replica + all branches from it
sprout connector resume prod
```

`connect` and `branch create` stream progress (NDJSON) so the CLI prints each step instead of hanging silently.

### Useful connect flags

| Flag | Meaning |
|------|---------|
| `--engine=postgres\|mongodb` | Override URL inference (`mongodb://` / `mongodb+srv://` → mongodb) |
| `--mode=logical\|physical` | Postgres: logical pub/sub or physical basebackup. Mongo is logical (dump snapshot) only |
| `--wipe` / `--no-wipe` | Rebootstrap (default) or resume existing replica |
| `--dry-run` | Estimate tables/rows without copying (Postgres logical) |
| `--tables=a,b` | Postgres table / Mongo collection allowlist |

### Hosted URLs

With subdomain + SNI proxy enabled on the server:

- Postgres `5432`: `<branch>-<github-login>-<connector>.<SPROUT_PUBLIC_HOST>`
- Mongo `27017` with `tls=true`: same hostname pattern

Replica/branch **data dirs stay creator-owned** (`data/replicas/<connector>-<login>`, `data/branches/<branch>-<login>-<connector>`). Sharing an org does **not** copy the dataset.

## Orgs

Connectors are **org-scoped**. Members of an org see the same connector row (same replica dir/port).

| Role | Can |
|------|-----|
| **owner** | members, connect/wipe/delete connector, all branches |
| **member** | list/status connectors; create/reset/delete **own** branches only |

The machine `SPROUT_TOKEN` acts as owner and sees everything.

```bash
sprout org list
sprout org create acme
sprout org use acme
sprout org members add teammate-login
sprout org members list
sprout org members remove teammate-login
sprout org delete acme          # blocked if it still has connectors; cannot delete personal default
```

Global `--org=` / `SPROUT_ORG` / `sprout org use` send `X-Sprout-Org`. GitHub users default to `default`. Machine tokens omit the header unless an org is set (many users can own an org named `default`).

## One-shot overrides

```bash
sprout --api-url=https://sprout.example.com --token=secret --org=acme health
```

Env vars:

- `SPROUT_API_URL` / `SPROUT_SERVER`
- `SPROUT_TOKEN`
- `SPROUT_ORG`
- `SPROUT_PROJECT` (default `default`)
- `SPROUT_CONFIG` (custom config path)

## SDK

```ts
import { SproutClient, saveConfig } from "sproutdb-cli";

saveConfig({ apiUrl: "https://sprout.example.com", token: "secret", org: "acme" });

const sprout = new SproutClient(); // loads ~/.sprout/config.json
await sprout.health();
await sprout.doctor();
await sprout.listOrgs();

const pg = await sprout.connect({
  url: "postgresql://...",
  name: "prod",
  mode: "logical",
  onProgress: console.log,
});

const mongo = await sprout.connect({
  url: "mongodb+srv://...",
  name: "atlas",
  engine: "mongodb",
});

const branch = await sprout.createBranch("preview", "prod", { onProgress: console.log });
console.log(branch.connection_string, branch.psql);

const diff = await sprout.diffBranch("preview", "prod");
console.log(diff.summary);
```

## Config precedence

1. Constructor / CLI flags (`--api-url`, `--token`, `--org`)
2. Env (`SPROUT_API_URL` / `SPROUT_SERVER`, `SPROUT_TOKEN`, `SPROUT_ORG`)
3. `~/.sprout/config.json`
4. Defaults (`http://127.0.0.1:8080`, `dev-token`; GitHub users also default org `default`)

## Commands

```text
sprout doctor
sprout init
sprout org list | create | use | delete | members ...
sprout connect [--name=...] [--engine=postgres|mongodb] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
sprout status [name]
sprout connector list | delete [--force] | suspend | resume <name>
sprout health
sprout login
sprout logout
sprout whoami
sprout branch create <name> [--from=<connector|main>]
sprout branch list
sprout branch get|diff|reset|delete|suspend|resume <name> [--from=<connector>]
sprout config set|get|unset|path ...
```

The Go `sprout-server` owns the data plane (replicas, CoW clones, Postgres/Mongo processes). This package is the client only.

## License

MIT — see the [Sprout repository](https://github.com/adityaraj-09/sprout).

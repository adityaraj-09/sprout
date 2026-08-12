# sproutdb-cli

Node.js **CLI + SDK** for [Sprout](https://github.com/adityaraj-09/sprout) — talks to `sprout-server` over HTTP so you can connect production Postgres, branch databases, and copy ready-to-use connection strings.

Installs the **`sprout`** binary on your PATH.

**Repo:** [https://github.com/adityaraj-09/sprout](https://github.com/adityaraj-09/sprout)  
**npm:** [https://www.npmjs.com/package/sproutdb-cli](https://www.npmjs.com/package/sproutdb-cli)

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
sprout config set token dev-token          # match SPROUT_TOKEN on the server
sprout config get                         # show saved + effective config
```

Then:

```bash
sprout doctor
sprout health

# logical connect (e.g. Supabase) — wipe/rebootstrap by default
sprout connect --name=prod --mode=logical --dry-run 'postgresql://...'
sprout connect --name=prod --mode=logical 'postgresql://...'
sprout status prod

# branch from a connector
sprout branch create my-feature --from=prod
# prints:
#   postgresql://sprout@host:PORT/postgres
#   psql "postgresql://sprout@host:PORT/postgres"

sprout branch list
sprout branch diff my-feature
sprout branch delete my-feature

sprout connector list
sprout connector delete prod
sprout connector suspend prod   # stop replica + all branches from it
sprout connector resume prod
```

### Useful connect flags

| Flag | Meaning |
|------|---------|
| `--mode=logical\|physical` | Logical pub/sub or physical basebackup |
| `--wipe` / `--no-wipe` | Rebootstrap (default) or resume existing replica |
| `--dry-run` | Estimate tables/rows without copying (logical) |
| `--tables=a,b` | Logical table allowlist |

### One-shot overrides

```bash
sprout --api-url=https://sprout.example.com --token=secret health
```

Env vars:

- `SPROUT_API_URL` / `SPROUT_SERVER`
- `SPROUT_TOKEN`
- `SPROUT_PROJECT` (default `default`)
- `SPROUT_CONFIG` (custom config path)

## SDK

```ts
import { SproutClient, saveConfig } from "sproutdb-cli";

saveConfig({ apiUrl: "https://sprout.example.com", token: "secret" });

const sprout = new SproutClient(); // loads ~/.sprout/config.json
await sprout.health();
await sprout.doctor();

const branch = await sprout.createBranch("preview", "prod");
console.log(branch.connection_string, branch.psql);

const diff = await sprout.diffBranch("preview");
console.log(diff.summary);
```

## Config precedence

1. Constructor / CLI flags (`--api-url`, `--token`)
2. Env (`SPROUT_API_URL` / `SPROUT_SERVER`, `SPROUT_TOKEN`)
3. `~/.sprout/config.json`
4. Defaults (`http://127.0.0.1:8080`, `dev-token`)

## Commands

```text
sprout doctor
sprout init
sprout connect [--name=...] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
sprout status [name]
sprout connector list | delete | suspend | resume <name>
sprout health
sprout branch create <name> [--from=<connector|main>]
sprout branch list | get | diff | reset | delete | suspend | resume <name>
sprout config set|get|unset|path ...
```

The Go `sprout-server` owns the data plane (replicas, CoW clones, Postgres processes). This package is the client only.

## License

MIT — see the [Sprout repository](https://github.com/adityaraj-09/sprout).

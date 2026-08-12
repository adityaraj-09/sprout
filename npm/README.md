# sproutdb

Node.js **CLI + SDK** for [Sprout](../README.md) — talks to `sprout-server` over HTTP.

## Install

```bash
cd npm && npm install && npm link
# or: npm install -g sproutdb
```

## Set API URL once

```bash
sprout config set api-url http://127.0.0.1:8080
sprout config set token dev-token          # optional
sprout config get                         # show saved + effective
```

Saved to `~/.sprout/config.json` (mode `0600`). Then just:

```bash
sprout health
sprout branch list
```

Override for one shot: `sprout --api-url=https://other.example health`  
Or env: `SPROUT_API_URL`, `SPROUT_TOKEN`, `SPROUT_CONFIG` (custom config path).

## SDK

```ts
import { SproutClient, saveConfig } from "sproutdb";

saveConfig({ apiUrl: "https://sprout.example.com", token: "secret" });

// later — picks up ~/.sprout/config.json automatically
const sprout = new SproutClient();
await sprout.health();
```

## Precedence

1. Constructor / CLI flags (`--api-url`, `--token`)
2. Env (`SPROUT_API_URL` / `SPROUT_SERVER`, `SPROUT_TOKEN`)
3. `~/.sprout/config.json`
4. Defaults (`http://127.0.0.1:8080`, `dev-token`)

The Go server still owns data plane work. This package is the client only.

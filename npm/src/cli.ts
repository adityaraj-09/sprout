import {
  SproutClient,
  SproutError,
  configPath,
  loadConfig,
  saveConfig,
  unsetConfig,
} from "./index.js";
import type { SproutConfigFile } from "./config.js";

function usage(): never {
  console.error(`sprout — CLI (talks to sprout-server)

Usage:
  sprout [--api-url=<url>] [--token=<token>] <command> ...

Commands:
  sprout init
  sprout connect [--name=<id>] [--mode=logical|physical] <postgresql-url>
  sprout status [name]
  sprout connector list
  sprout health
  sprout branch create <name> [--from=<connector|main>]
  sprout branch list
  sprout branch get|reset|delete|suspend|resume <name>

Config (persisted in ~/.sprout/config.json):
  sprout config set api-url <url>
  sprout config set token <token>
  sprout config set project <name>
  sprout config get
  sprout config path
  sprout config unset api-url|token|project

Global flags (override saved config for one command):
  --api-url=<url>   also --server=
  --token=<token>

Precedence: flags → env → config file → defaults
  SPROUT_API_URL / SPROUT_SERVER
  SPROUT_TOKEN
  SPROUT_CONFIG   (path to config file)
`);
  process.exit(2);
}

function fatal(err: unknown): never {
  if (err instanceof SproutError) {
    console.error(`error: ${err.code}: ${err.message}`);
  } else {
    console.error(`error: ${err instanceof Error ? err.message : String(err)}`);
  }
  process.exit(1);
}

function flag(args: string[], name: string): string | undefined {
  const prefix = `--${name}=`;
  const eq = args.find((a) => a.startsWith(prefix));
  if (eq) return eq.slice(prefix.length);
  const idx = args.indexOf(`--${name}`);
  if (idx >= 0 && args[idx + 1] && !args[idx + 1]!.startsWith("-")) {
    return args[idx + 1];
  }
  return undefined;
}

function takeGlobals(argv: string[]): {
  apiUrl?: string;
  token?: string;
  rest: string[];
} {
  const rest: string[] = [];
  let apiUrl: string | undefined;
  let token: string | undefined;
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]!;
    if (a.startsWith("--api-url=")) {
      apiUrl = a.slice("--api-url=".length);
      continue;
    }
    if (a === "--api-url" && argv[i + 1] && !argv[i + 1]!.startsWith("-")) {
      apiUrl = argv[++i];
      continue;
    }
    if (a.startsWith("--server=")) {
      apiUrl = a.slice("--server=".length);
      continue;
    }
    if (a === "--server" && argv[i + 1] && !argv[i + 1]!.startsWith("-")) {
      apiUrl = argv[++i];
      continue;
    }
    if (a.startsWith("--token=")) {
      token = a.slice("--token=".length);
      continue;
    }
    if (a === "--token" && argv[i + 1] && !argv[i + 1]!.startsWith("-")) {
      token = argv[++i];
      continue;
    }
    rest.push(a);
  }
  return { apiUrl, token, rest };
}

function positional(args: string[]): string[] {
  return args.filter((a) => !a.startsWith("-"));
}

function handleConfig(argv: string[]): void {
  const sub = argv[1];
  if (!sub) usage();
  switch (sub) {
    case "path":
      console.log(configPath());
      return;
    case "get": {
      const cfg = loadConfig();
      const client = new SproutClient();
      console.log(
        JSON.stringify(
          {
            file: configPath(),
            saved: cfg,
            effective: {
              apiUrl: client.baseUrl,
              token: client.token === "dev-token" ? "dev-token" : "***",
              project: client.project,
            },
          },
          null,
          2,
        ),
      );
      return;
    }
    case "set": {
      const key = argv[2];
      const value = argv[3];
      if (!key || value === undefined) {
        fatal(new Error("usage: sprout config set api-url|token|project <value>"));
      }
      const patch: SproutConfigFile = {};
      if (key === "api-url" || key === "apiUrl" || key === "server") {
        patch.apiUrl = value.replace(/\/$/, "");
      } else if (key === "token") {
        patch.token = value;
      } else if (key === "project") {
        patch.project = value;
      } else {
        fatal(new Error(`unknown config key: ${key} (use api-url, token, project)`));
      }
      const saved = saveConfig(patch);
      console.log(`✓ saved ${configPath()}`);
      console.log(JSON.stringify(saved, null, 2));
      return;
    }
    case "unset": {
      const key = argv[2];
      if (!key) fatal(new Error("usage: sprout config unset api-url|token|project"));
      const map: Record<string, keyof SproutConfigFile> = {
        "api-url": "apiUrl",
        apiUrl: "apiUrl",
        server: "apiUrl",
        token: "token",
        project: "project",
      };
      const field = map[key];
      if (!field) fatal(new Error(`unknown config key: ${key}`));
      const saved = unsetConfig(field);
      console.log(`✓ unset ${key} in ${configPath()}`);
      console.log(JSON.stringify(saved, null, 2));
      return;
    }
    default:
      usage();
  }
}

async function main(): Promise<void> {
  const raw = process.argv.slice(2);
  if (raw.length === 0) usage();

  const { apiUrl, token, rest: argv } = takeGlobals(raw);
  if (argv.length === 0) usage();

  if (argv[0] === "config") {
    handleConfig(argv);
    return;
  }

  const client = new SproutClient({ apiUrl, token });
  const cmd = argv[0]!;

  try {
    switch (cmd) {
      case "init": {
        const proj = await client.init();
        console.log(`✓ project ${proj.name} (${proj.id})`);
        break;
      }
      case "connect": {
        const rest = argv.slice(1);
        let mode = flag(rest, "mode") ?? "physical";
        if (rest.includes("--logical")) mode = "logical";
        if (rest.includes("--physical")) mode = "physical";
        const name = flag(rest, "name") ?? "primary";
        const url = positional(rest)[0];
        if (!url) {
          fatal(
            new Error(
              "usage: sprout connect [--name=<id>] [--mode=logical|physical] <postgresql-url>",
            ),
          );
        }
        const out = await client.connect({ url, name, mode });
        console.log(JSON.stringify(out, null, 2));
        break;
      }
      case "status": {
        const name = positional(argv.slice(1))[0];
        const out = await client.replication(name);
        console.log(JSON.stringify(out, null, 2));
        break;
      }
      case "connector": {
        if (argv[1] !== "list") usage();
        const list = await client.listConnectors();
        if (list.length === 0) {
          console.log("(no connectors)");
          break;
        }
        for (const conn of list) {
          console.log(
            `${conn.name.padEnd(12)} ${conn.mode.padEnd(10)} ${conn.status.padEnd(14)} port=${String(conn.port).padEnd(5)} lag_bytes=${conn.last_lag_bytes} lsn=${conn.last_lsn ?? ""}\n  dir=${conn.data_dir}\n  ${conn.primary_url}`,
          );
        }
        break;
      }
      case "health": {
        const out = await client.health();
        console.log(out.status);
        break;
      }
      case "branch": {
        const sub = argv[1];
        if (!sub) usage();
        switch (sub) {
          case "create": {
            const rest = argv.slice(2);
            const from = flag(rest, "from");
            const name = positional(rest)[0];
            if (!name) {
              fatal(new Error("usage: sprout branch create <name> [--from=<connector>]"));
            }
            const rec = await client.createBranch(name, from);
            const src = rec.source_connector || "main";
            console.log(`✓ ${rec.name} [${rec.status}] from=${src}\n  ${rec.connection_string}`);
            break;
          }
          case "list": {
            const list = await client.listBranches();
            for (const b of list) {
              if (b.role === "branch") {
                const src = b.source_connector || "-";
                console.log(
                  `${b.name.padEnd(16)} ${b.status.padEnd(10)} port=${String(b.port).padEnd(5)} from=${src.padEnd(12)} ${b.connection_string}`,
                );
              } else {
                console.log(
                  `${b.name.padEnd(16)} ${b.status.padEnd(10)} port=${String(b.port).padEnd(5)} role=${b.role.padEnd(8)} ${b.connection_string}`,
                );
              }
            }
            break;
          }
          case "get": {
            const name = argv[2];
            if (!name) usage();
            console.log(JSON.stringify(await client.getBranch(name), null, 2));
            break;
          }
          case "reset": {
            const name = argv[2];
            if (!name) usage();
            const rec = await client.resetBranch(name);
            console.log(`✓ reset ${rec.name}\n  ${rec.connection_string}`);
            break;
          }
          case "delete": {
            const name = argv[2];
            if (!name) usage();
            await client.deleteBranch(name);
            console.log(`✓ deleted ${name}`);
            break;
          }
          case "suspend": {
            const name = argv[2];
            if (!name) usage();
            const rec = await client.suspendBranch(name);
            console.log(`✓ suspended ${rec.name} (status=${rec.status})`);
            break;
          }
          case "resume": {
            const name = argv[2];
            if (!name) usage();
            const rec = await client.resumeBranch(name);
            console.log(`✓ resumed ${rec.name}\n  ${rec.connection_string}`);
            break;
          }
          default:
            usage();
        }
        break;
      }
      default:
        usage();
    }
  } catch (err) {
    fatal(err);
  }
}

main();

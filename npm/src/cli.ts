import {
  SproutClient,
  SproutError,
  configPath,
  loadConfig,
  saveConfig,
  unsetConfig,
} from "./index.js";
import type { SproutConfigFile } from "./config.js";
import { browserURL, openBrowser, requestDeviceCode, waitForToken } from "./github.js";

function usage(): never {
  console.error(`sprout — CLI (talks to sprout-server)

Usage:
  sprout [--api-url=<url>] [--token=<token>] <command> ...

Commands:
  sprout login
  sprout logout
  sprout whoami
  sprout doctor
  sprout init
  sprout connect [--name=<id>] [--engine=postgres|mysql] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>
  sprout status [name]
  sprout connector list
  sprout connector delete <name> [--force]
  sprout connector suspend|resume <name>
  sprout health
  sprout branch create <name> [--from=<connector|main>]
  sprout branch list
  sprout branch get|diff|reset|delete|suspend|resume <name> [--from=<connector>]

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

function parseNameFrom(args: string[]): { name?: string; from?: string } {
  return { name: positional(args)[0], from: flag(args, "from") };
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
      const saved = { ...cfg };
      if (saved.token) saved.token = "***";
      console.log(
        JSON.stringify(
          {
            file: configPath(),
            saved,
            effective: {
              apiUrl: client.baseUrl,
              token: client.token === "dev-token" ? "dev-token" : "***",
              project: client.project,
              githubLogin: cfg.githubLogin,
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
      const printed = { ...saved };
      if (printed.token) printed.token = "***";
      console.log(JSON.stringify(printed, null, 2));
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

  if (argv[0] === "login") {
    try {
      await runLogin(apiUrl);
    } catch (err) {
      fatal(err);
    }
    return;
  }
  if (argv[0] === "logout") {
    unsetConfig("token", "githubLogin");
    console.log(`✓ logged out (${configPath()})`);
    return;
  }

  const client = new SproutClient({ apiUrl, token });
  const cmd = argv[0]!;

  try {
    switch (cmd) {
      case "doctor": {
        const rep = await client.doctor();
        console.log(rep.ok ? "✓ doctor ok" : "✗ doctor found problems");
        for (const ch of rep.checks) {
          const mark = !ch.ok ? "✗" : ch.level === "warn" ? "!" : "✓";
          console.log(`${mark} ${ch.name.padEnd(16)} ${ch.detail}`);
          if (ch.hint) console.log(`    hint: ${ch.hint}`);
        }
        if (!rep.ok) process.exit(1);
        break;
      }
      case "init": {
        const proj = await client.init();
        console.log(`✓ project ${proj.name} (${proj.id})`);
        break;
      }
      case "connect": {
        const rest = argv.slice(1);
        let mode = flag(rest, "mode") ?? "";
        if (rest.includes("--logical")) mode = "logical";
        if (rest.includes("--physical")) mode = "physical";
        const engineName = flag(rest, "engine");
        const name = flag(rest, "name") ?? "primary";
        const wipe = !rest.includes("--no-wipe");
        const dryRun = rest.includes("--dry-run");
        const tablesRaw = flag(rest, "tables");
        const tables = tablesRaw
          ? tablesRaw
              .split(",")
              .map((t) => t.trim())
              .filter(Boolean)
          : undefined;
        const url = positional(rest)[0];
        if (!url) {
          fatal(
            new Error(
              "usage: sprout connect [--name=<id>] [--engine=postgres|mysql] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] <url>",
            ),
          );
        }
        const out = await client.connect({ url, name, engine: engineName, mode, wipe, dryRun, tables });
        if (out.dry_run) {
          console.log("dry-run estimate (will hit prod once for real bootstrap):");
          console.log(JSON.stringify(out.estimate, null, 2));
          break;
        }
        if (out.connection_string) {
          console.log("✓ connected");
          console.log(`  ${out.connection_string}`);
          if (out.psql) console.log(`  ${out.psql}`);
        }
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
        const sub = argv[1];
        if (sub === "list") {
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
        if (sub === "delete") {
          const rest = argv.slice(2);
          const force = rest.includes("--force");
          const name = positional(rest)[0];
          if (!name) usage();
          await client.deleteConnector(name, { force });
          console.log(`✓ deleted connector ${name}`);
          break;
        }
        if (sub === "suspend" || sub === "resume") {
          const name = argv[2];
          if (!name) usage();
          const out =
            sub === "suspend"
              ? await client.suspendConnector(name)
              : await client.resumeConnector(name);
          console.log(`✓ ${out.message ?? `${sub}ed connector ${name}`}`);
          console.log(JSON.stringify(out, null, 2));
          break;
        }
        usage();
        break;
      }
      case "health": {
        const out = await client.health();
        console.log(out.status);
        break;
      }
      case "whoami": {
        const who = await client.whoami();
        console.log(`${who.kind} ${who.login}`);
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
            if (rec.psql) console.log(`  ${rec.psql}`);
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
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch get <name> [--from=<connector>]"));
            }
            console.log(JSON.stringify(await client.getBranch(name, from), null, 2));
            break;
          }
          case "diff": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch diff <name> [--from=<connector>]"));
            }
            const diff = await client.diffBranch(name, from);
            console.log(diff.summary);
            console.log(JSON.stringify(diff, null, 2));
            break;
          }
          case "reset": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch reset <name> [--from=<connector>]"));
            }
            const rec = await client.resetBranch(name, from);
            console.log(`✓ reset ${rec.name}\n  ${rec.connection_string}`);
            break;
          }
          case "delete": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch delete <name> [--from=<connector>]"));
            }
            await client.deleteBranch(name, from);
            console.log(`✓ deleted ${name}`);
            break;
          }
          case "suspend": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch suspend <name> [--from=<connector>]"));
            }
            const rec = await client.suspendBranch(name, from);
            console.log(`✓ suspended ${rec.name} (status=${rec.status})`);
            break;
          }
          case "resume": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch resume <name> [--from=<connector>]"));
            }
            const rec = await client.resumeBranch(name, from);
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

async function runLogin(apiUrl?: string): Promise<void> {
  const probe = new SproutClient({ apiUrl, ignoreConfigFile: false });
  let meta;
  try {
    meta = await probe.githubAuth();
  } catch (err) {
    if (err instanceof SproutError && err.status === 404) {
      throw new Error(`github login is not enabled on ${probe.baseUrl} — set SPROUT_GITHUB_CLIENT_ID on the server`);
    }
    throw err;
  }
  if (!meta.enabled) {
    throw new Error(`github login is not enabled on ${probe.baseUrl}`);
  }

  const dc = await requestDeviceCode(meta);
  const page = browserURL(dc);
  console.log("GitHub device login\n");
  console.log(`  1. Open  ${page}`);
  console.log(`  2. Enter code  ${dc.user_code}\n`);
  try {
    await openBrowser(page);
  } catch (err) {
    console.error(`  (could not open a browser: ${err instanceof Error ? err.message : err})`);
  }
  console.log("Waiting for GitHub…");
  const token = await waitForToken(meta, dc);
  const identified = new SproutClient({ apiUrl: probe.baseUrl, token, ignoreConfigFile: true });
  const who = await identified.whoami();
  saveConfig({
    apiUrl: probe.baseUrl,
    token,
    githubLogin: who.login || who.kind,
  });
  console.log(`✓ logged in as ${who.login || who.kind} (${configPath()})`);
}

main();

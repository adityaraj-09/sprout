import {
  SproutClient,
  SproutError,
  configPath,
  loadConfig,
  saveConfig,
  unsetConfig,
} from "./index.js";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";
import type { SproutConfigFile } from "./config.js";
import type { BranchRecord, PreflightReport } from "./types.js";
import { browserURL, openBrowser, requestDeviceCode, waitForToken } from "./github.js";

function usage(): never {
  console.error(`sprout — CLI (talks to sprout-server)

Usage:
  sprout [--api-url=<url>] [--token=<token>] [--org=<name>] [--format=json] [--print-url] [--quiet] <command> ...

Commands:
  sprout login
  sprout logout
  sprout whoami
  sprout doctor
  sprout org list|create|use|delete ...
  sprout org members list|add|remove ...
  sprout init
  sprout preflight [--engine=...] [--mode=logical|physical] [--tables=a,b] <url>
  sprout connect [--name=<id>] [--engine=postgres|mongodb|qdrant] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] [--branch-sql=@file|SQL] <url>
  sprout status [name]
  sprout sync [name]
  sprout connector list
  sprout connector preflight ...
  sprout connector hook <name> --sql=@file.sql|--clear
  sprout connector delete <name> [--force]
  sprout connector suspend|resume <name>
  sprout env [name] [--from=<connector>] [--write=.env.sprout]
  sprout url [name] [--from=<connector>]
  sprout health
  sprout branch create <name> [--from=<connector|main>]
  sprout branch switch <name> [--from=<connector>]
  sprout branch list
  sprout branch get|diff|reset|delete|suspend|resume <name> [--from=<connector>]

Config (persisted in ~/.sprout/config.json):
  sprout config set api-url <url>
  sprout config set token <token>
  sprout config set project <name>
  sprout config set org <name>
  sprout config get
  sprout config path
  sprout config unset api-url|token|project|org

Global flags (override saved config for one command):
  --api-url=<url>   also --server=
  --token=<token>
  --org=<name>      current org (X-Sprout-Org); GitHub users default to "default"
  --format=json     machine JSON
  --print-url       stdout is only the connection string
  --quiet / -q      hide progress

Precedence: flags → env → config file → defaults
  SPROUT_API_URL / SPROUT_SERVER
  SPROUT_TOKEN
  SPROUT_ORG
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
  org?: string;
  format: string;
  printUrl: boolean;
  quiet: boolean;
  rest: string[];
} {
  const rest: string[] = [];
  let apiUrl: string | undefined;
  let token: string | undefined;
  let org: string | undefined;
  let format = "text";
  let printUrl = false;
  let quiet = false;
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
    if (a.startsWith("--org=")) {
      org = a.slice("--org=".length);
      continue;
    }
    if (a === "--org" && argv[i + 1] && !argv[i + 1]!.startsWith("-")) {
      org = argv[++i];
      continue;
    }
    if (a.startsWith("--format=")) {
      format = a.slice("--format=".length).toLowerCase() === "json" ? "json" : "text";
      continue;
    }
    if (a === "--format" && argv[i + 1] && !argv[i + 1]!.startsWith("-")) {
      format = argv[++i]!.toLowerCase() === "json" ? "json" : "text";
      continue;
    }
    if (a === "--print-url" || a === "--print_url") {
      printUrl = true;
      continue;
    }
    if (a === "--quiet" || a === "-q") {
      quiet = true;
      continue;
    }
    rest.push(a);
  }
  return { apiUrl, token, org, format, printUrl, quiet, rest };
}

type Out = { format: string; printUrl: boolean; quiet: boolean };

function emitJSON(v: unknown): void {
  console.log(JSON.stringify(v, null, 2));
}

function connStringOf(v: unknown): string {
  if (typeof v === "string") return v;
  if (v && typeof v === "object" && "connection_string" in v) {
    const cs = (v as { connection_string?: string }).connection_string;
    if (cs) return cs;
  }
  return "";
}

function writeOut(out: Out, v: unknown, human: () => void): void {
  if (out.printUrl) {
    const cs = connStringOf(v);
    if (cs) {
      console.log(cs);
      return;
    }
  }
  if (out.format === "json") {
    emitJSON(v);
    return;
  }
  human();
}

function onProgress(out: Out): ((msg: string) => void) | undefined {
  if (out.quiet || out.format === "json" || out.printUrl) return undefined;
  return (msg) => console.log(msg);
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
              org: client.org || undefined,
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
        fatal(new Error("usage: sprout config set api-url|token|project|org <value>"));
      }
      const patch: SproutConfigFile = {};
      if (key === "api-url" || key === "apiUrl" || key === "server") {
        patch.apiUrl = value.replace(/\/$/, "");
      } else if (key === "token") {
        patch.token = value;
      } else if (key === "project") {
        patch.project = value;
      } else if (key === "org") {
        patch.org = value;
      } else {
        fatal(new Error(`unknown config key: ${key} (use api-url, token, project, org)`));
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
      if (!key) fatal(new Error("usage: sprout config unset api-url|token|project|org"));
      const map: Record<string, keyof SproutConfigFile> = {
        "api-url": "apiUrl",
        apiUrl: "apiUrl",
        server: "apiUrl",
        token: "token",
        project: "project",
        org: "org",
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

  const { apiUrl, token, org, format, printUrl, quiet, rest: argv } = takeGlobals(raw);
  if (argv.length === 0) usage();
  const out: Out = { format, printUrl, quiet };

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

  const client = new SproutClient({ apiUrl, token, org });
  const cmd = argv[0]!;

  try {
    switch (cmd) {
      case "doctor": {
        const rep = await client.doctor();
        if (out.format === "json") {
          emitJSON(rep);
          if (!rep.ok) process.exit(1);
          break;
        }
        console.log(rep.ok ? "✓ doctor ok" : "✗ doctor found problems");
        for (const ch of rep.checks) {
          const mark = !ch.ok ? "✗" : ch.level === "warn" ? "!" : "✓";
          console.log(`${mark} ${ch.name.padEnd(16)} ${ch.detail}`);
          if (ch.hint) console.log(`    hint: ${ch.hint}`);
        }
        if (!rep.ok) process.exit(1);
        break;
      }
      case "org": {
        const sub = argv[1];
        if (!sub) usage();
        if (sub === "list") {
          const out = await client.listOrgs();
          if (!out.orgs?.length) {
            console.log("(no orgs)");
            break;
          }
          const cur = out.current_org || client.org;
          for (const o of out.orgs) {
            const mark = o.name === cur || o.id === cur || o.id === out.current_id ? "*" : " ";
            console.log(
              `${mark} ${o.name.padEnd(16)} ${(o.role || "-").padEnd(8)} owner=${(o.created_by || "").padEnd(16)} ${o.id}`,
            );
          }
          break;
        }
        if (sub === "create") {
          const name = argv[2];
          if (!name) fatal(new Error("usage: sprout org create <name>"));
          const orgRec = await client.createOrg(name);
          console.log(`✓ created org ${orgRec.name} (${orgRec.id})`);
          break;
        }
        if (sub === "use") {
          const name = argv[2];
          if (!name) fatal(new Error("usage: sprout org use <name-or-id>"));
          const saved = saveConfig({ org: name });
          console.log(`✓ current org ${saved.org} (${configPath()})`);
          break;
        }
        if (sub === "delete") {
          const name = argv[2];
          if (!name) fatal(new Error("usage: sprout org delete <name-or-id>"));
          await client.deleteOrg(name);
          console.log(`✓ deleted org ${name}`);
          break;
        }
        if (sub === "members") {
          const action = argv[2];
          const orgName = client.org || "default";
          if (action === "list") {
            const named = argv[3] && !argv[3].startsWith("-") ? argv[3] : orgName;
            const list = await client.listOrgMembers(named);
            if (list.length === 0) {
              console.log("(no members)");
              break;
            }
            for (const m of list) {
              console.log(`${m.login.padEnd(16)} ${m.role.padEnd(8)} added_by=${m.added_by ?? ""}`);
            }
            break;
          }
          if (action === "add") {
            const login = argv[3];
            if (!login) fatal(new Error("usage: sprout org members add <github-login>"));
            const m = await client.addOrgMember(login, orgName);
            console.log(`✓ added ${m.login} as ${m.role}`);
            console.log(JSON.stringify(m, null, 2));
            break;
          }
          if (action === "remove") {
            const login = argv[3];
            if (!login) fatal(new Error("usage: sprout org members remove <github-login>"));
            await client.removeOrgMember(login, orgName);
            console.log(`✓ removed ${login} from ${orgName}`);
            break;
          }
        }
        usage();
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
        const branchSql = readAtFlag(flag(rest, "branch-sql"));
        const url = positional(rest)[0];
        if (!url) {
          fatal(
            new Error(
              "usage: sprout connect [--name=<id>] [--engine=postgres|mongodb|qdrant] [--mode=logical|physical] [--wipe|--no-wipe] [--dry-run] [--tables=a,b] [--branch-sql=@file|SQL] <url>",
            ),
          );
        }
        const connOut = await client.connect({
          url,
          name,
          engine: engineName,
          mode: mode || undefined,
          wipe,
          dryRun,
          tables,
          branchSql,
          onProgress: onProgress(out),
        });
        if (connOut.dry_run) {
          if (out.format === "json") {
            emitJSON(connOut);
            break;
          }
          console.log("dry-run estimate (will hit prod once for real bootstrap):");
          console.log(JSON.stringify(connOut.estimate, null, 2));
          break;
        }
        writeOut(out, connOut, () => {
          if (connOut.connection_string) {
            console.log("✓ connected");
            console.log(`  ${connOut.connection_string}`);
            if (connOut.psql) console.log(`  ${connOut.psql}`);
            if (connOut.mongosh) console.log(`  ${connOut.mongosh}`);
            if (connOut.curl) console.log(`  ${connOut.curl}`);
          }
        });
        break;
      }
      case "preflight": {
        await runPreflight(client, argv.slice(1), out);
        break;
      }
      case "env": {
        await runEnvCmd(client, argv.slice(1), out);
        break;
      }
      case "url": {
        await runEnvCmd(client, argv.slice(1), { ...out, printUrl: true });
        break;
      }
      case "status": {
        const name = positional(argv.slice(1))[0];
        const st = await client.replication(name);
        writeOut(out, st, () => emitJSON(st));
        break;
      }
      case "sync": {
        const name = positional(argv.slice(1))[0];
        const syncOut = await client.sync(name, { onProgress: onProgress(out) });
        writeOut(out, syncOut, () => {
          console.log("✓ synced");
          if (syncOut.message) console.log(`  ${syncOut.message}`);
        });
        break;
      }
      case "connector": {
        const sub = argv[1];
        if (sub === "list") {
          const list = await client.listConnectors();
          if (out.format === "json") {
            emitJSON(list);
            break;
          }
          if (list.length === 0) {
            console.log("(no connectors)");
            break;
          }
          for (const conn of list) {
            const eng = conn.engine || "postgres";
            const hook = conn.branch_sql ? " hook" : "";
            console.log(
              `${statusMark(conn.status)} ${conn.name.padEnd(14)} ${eng.padEnd(8)} ${conn.mode.padEnd(10)} ${conn.status.padEnd(14)} :${String(conn.port).padEnd(5)}${hook}`,
            );
            if (conn.primary_url) console.log(`    ${conn.primary_url}`);
          }
          break;
        }
        if (sub === "preflight") {
          await runPreflight(client, argv.slice(2), out);
          break;
        }
        if (sub === "hook") {
          const rest = argv.slice(2);
          const name = positional(rest)[0];
          const clear = rest.includes("--clear");
          const sql = readAtFlag(flag(rest, "sql")) ?? "";
          if (!name || (!clear && !sql)) {
            fatal(new Error("usage: sprout connector hook <name> --sql=@file.sql|--clear"));
          }
          const rec = await client.setConnectorHook(name, clear ? "" : sql);
          writeOut(out, rec, () => {
            console.log(clear ? `✓ cleared branch_sql on ${name}` : `✓ set branch_sql on ${name} (${sql.length} bytes)`);
          });
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
          const resumeOut =
            sub === "suspend"
              ? await client.suspendConnector(name)
              : await client.resumeConnector(name);
          writeOut(out, resumeOut, () => {
            console.log(`✓ ${resumeOut.message ?? `${sub}ed connector ${name}`}`);
          });
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
        console.log(`${who.kind} ${who.login}${who.org ? ` org=${who.org}` : ""}`);
        for (const o of who.orgs ?? []) {
          console.log(`  ${(o.name || "").padEnd(16)} ${(o.role || "-").padEnd(8)} ${o.id}`);
        }
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
            const rec = await client.createBranch(name, from, { onProgress: onProgress(out) });
            writeOut(out, rec, () => {
              const src = rec.source_connector || "main";
              console.log(`✓ ${rec.name} [${rec.status}] from=${src}\n  ${rec.connection_string}`);
              if (rec.psql) console.log(`  ${rec.psql}`);
              if (rec.mongosh) console.log(`  ${rec.mongosh}`);
              if (rec.curl) console.log(`  ${rec.curl}`);
            });
            break;
          }
          case "switch": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) fatal(new Error("usage: sprout branch switch <name> [--from=<connector>]"));
            saveConfig({ currentBranch: name, currentFrom: from ?? "" });
            console.log(`✓ current branch ${name}${from ? ` --from=${from}` : ""} (${configPath()})`);
            break;
          }
          case "list": {
            const list = await client.listBranches();
            if (out.format === "json") {
              emitJSON(list);
              break;
            }
            const cur = loadConfig();
            if (list.length === 0) {
              console.log("(no branches)");
              break;
            }
            for (const b of list) {
              const star =
                b.name === cur.currentBranch &&
                (!cur.currentFrom || b.source_connector === cur.currentFrom || b.role !== "branch")
                  ? "*"
                  : " ";
              const src = b.source_connector || b.role;
              console.log(
                `${star} ${statusMark(b.status)} ${b.name.padEnd(16)} ${b.status.padEnd(10)} from=${(src || "-").padEnd(12)} ${b.connection_string}`,
              );
            }
            break;
          }
          case "get": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch get <name> [--from=<connector>]"));
            }
            const rec = await client.getBranch(name, from);
            writeOut(out, rec, () => emitJSON(rec));
            break;
          }
          case "diff": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch diff <name> [--from=<connector>]"));
            }
            const diff = await client.diffBranch(name, from);
            if (out.format === "json") {
              emitJSON(diff);
              break;
            }
            console.log(diff.summary);
            break;
          }
          case "reset": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch reset <name> [--from=<connector>]"));
            }
            const rec = await client.resetBranch(name, from);
            writeOut(out, rec, () => console.log(`✓ reset ${rec.name}\n  ${rec.connection_string}`));
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
            writeOut(out, rec, () => console.log(`✓ suspended ${rec.name} (status=${rec.status})`));
            break;
          }
          case "resume": {
            const { name, from } = parseNameFrom(argv.slice(2));
            if (!name) {
              fatal(new Error("usage: sprout branch resume <name> [--from=<connector>]"));
            }
            const rec = await client.resumeBranch(name, from);
            writeOut(out, rec, () => console.log(`✓ resumed ${rec.name}\n  ${rec.connection_string}`));
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

function readAtFlag(v: string | undefined): string | undefined {
  if (v === undefined) return undefined;
  if (!v.startsWith("@")) return v;
  return readFileSync(v.slice(1), "utf8");
}

function statusMark(status: string): string {
  switch ((status || "").toLowerCase()) {
    case "active":
    case "replicating":
      return "●";
    case "idle":
      return "○";
    case "error":
    case "crashed":
      return "✗";
    default:
      return "·";
  }
}

function envLines(cs: string): string[] {
  if (cs.startsWith("mongodb")) return [`MONGODB_URI=${cs}`, `DATABASE_URL=${cs}`];
  if (cs.startsWith("http://") || cs.startsWith("https://")) {
    const lines = [`QDRANT_URL=${cs}`];
    const m = cs.match(/api[-_]?key=([^&]+)/i);
    if (m) lines.push(`QDRANT_API_KEY=${m[1]}`);
    return lines;
  }
  return [`DATABASE_URL=${cs}`];
}

async function runPreflight(client: SproutClient, args: string[], out: Out): Promise<void> {
  let mode = flag(args, "mode") ?? "";
  if (args.includes("--logical")) mode = "logical";
  if (args.includes("--physical")) mode = "physical";
  const engineName = flag(args, "engine");
  const tablesRaw = flag(args, "tables");
  const tables = tablesRaw
    ? tablesRaw
        .split(",")
        .map((t) => t.trim())
        .filter(Boolean)
    : undefined;
  const url = positional(args)[0];
  if (!url) {
    fatal(new Error("usage: sprout connector preflight [--engine=...] [--mode=logical|physical] [--tables=a,b] <url>"));
  }
  const rep: PreflightReport = await client.preflight({
    url,
    engine: engineName,
    mode: mode || undefined,
    tables,
  });
  if (out.format === "json") {
    emitJSON(rep);
    if (!rep.ok) process.exit(1);
    return;
  }
  console.log(rep.ok ? "✓ preflight ok — nothing was created" : "✗ preflight found problems — nothing was created");
  console.log(`  engine=${rep.engine} mode=${rep.mode}`);
  for (const ch of rep.checks) {
    console.log(`${ch.ok ? "✓" : "✗"} ${(ch.name || "").padEnd(18)} ${ch.detail}`);
    if (ch.hint) console.log(`    hint: ${ch.hint}`);
    if (ch.sql) console.log(`    sql:  ${ch.sql}`);
  }
  if (rep.fix_sql?.length) {
    console.log("fix SQL:");
    for (const s of rep.fix_sql) console.log(`  ${s}`);
  }
  if (!rep.ok) process.exit(1);
}

async function runEnvCmd(client: SproutClient, args: string[], out: Out): Promise<void> {
  let write = flag(args, "write");
  if (args.includes("--write") && write === undefined) write = ".env.sprout";
  const cfg = loadConfig();
  const from = flag(args, "from");
  const name = positional(args)[0] ?? cfg.currentBranch;
  const src = from ?? cfg.currentFrom;
  if (!name) {
    fatal(new Error("no current branch — sprout branch switch <name> or pass a name"));
  }
  const rec: BranchRecord = await client.getBranch(name, src);
  if (out.printUrl) {
    console.log(rec.connection_string);
    return;
  }
  const lines = envLines(rec.connection_string);
  if (out.format === "json") {
    emitJSON({ name: rec.name, from: rec.source_connector, connection_string: rec.connection_string, env: lines });
    return;
  }
  if (write) {
    mkdirSync(dirname(write) === "." ? process.cwd() : dirname(write), { recursive: true });
    writeFileSync(write, lines.join("\n") + "\n", { mode: 0o600 });
    console.log(`✓ wrote ${write}`);
  }
  for (const l of lines) console.log(l);
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

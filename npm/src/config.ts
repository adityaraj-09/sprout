import { homedir } from "node:os";
import { mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";

export type SproutConfigFile = {
  apiUrl?: string;
  token?: string;
  project?: string;
  githubLogin?: string;
};

/** ~/.sprout/config.json (override with SPROUT_CONFIG) */
export function configPath(): string {
  if (process.env.SPROUT_CONFIG) return process.env.SPROUT_CONFIG;
  return join(homedir(), ".sprout", "config.json");
}

export function loadConfig(): SproutConfigFile {
  const path = configPath();
  if (!existsSync(path)) return {};
  try {
    const raw = readFileSync(path, "utf8");
    const data = JSON.parse(raw) as SproutConfigFile;
    return data && typeof data === "object" ? data : {};
  } catch {
    return {};
  }
}

export function saveConfig(patch: SproutConfigFile): SproutConfigFile {
  const path = configPath();
  const next: SproutConfigFile = { ...loadConfig(), ...patch };
  // Drop empty string keys so defaults apply again
  for (const key of Object.keys(next) as (keyof SproutConfigFile)[]) {
    if (next[key] === "" || next[key] === undefined) {
      delete next[key];
    }
  }
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, JSON.stringify(next, null, 2) + "\n", { mode: 0o600 });
  return next;
}

export function unsetConfig(...keys: (keyof SproutConfigFile)[]): SproutConfigFile {
  const cur = loadConfig();
  for (const k of keys) delete cur[k];
  mkdirSync(dirname(configPath()), { recursive: true });
  writeFileSync(configPath(), JSON.stringify(cur, null, 2) + "\n", { mode: 0o600 });
  return cur;
}

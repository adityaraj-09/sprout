import {
  BranchDiff,
  BranchRecord,
  ConnectResult,
  Connector,
  DoctorReport,
  Org,
  OrgList,
  OrgMember,
  ProgressHandler,
  Project,
  ReplicationStatus,
  SproutClientOptions,
  SproutError,
  WhoAmI,
} from "./types.js";
import { loadConfig } from "./config.js";
import type { GitHubAuthMeta } from "./github.js";

export class SproutClient {
  readonly baseUrl: string;
  readonly token: string;
  readonly project: string;
  readonly org: string;
  readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;

  constructor(opts: SproutClientOptions = {}) {
    const file = opts.ignoreConfigFile ? {} : loadConfig();
    const fromOpts = opts.apiUrl ?? opts.baseUrl;
    const fromEnv = process.env.SPROUT_API_URL ?? process.env.SPROUT_SERVER;
    this.baseUrl = (fromOpts ?? fromEnv ?? file.apiUrl ?? "http://127.0.0.1:8080").replace(
      /\/$/,
      "",
    );
    this.token = resolveToken(opts.token ?? process.env.SPROUT_TOKEN ?? file.token, this.baseUrl);
    this.project = opts.project ?? process.env.SPROUT_PROJECT ?? file.project ?? "default";
    this.org = (
      opts.org ??
      process.env.SPROUT_ORG ??
      file.org ??
      (file.githubLogin ? "default" : "")
    ).trim();
    this.timeoutMs = opts.timeoutMs ?? 60 * 60 * 1000;
    this.fetchImpl = opts.fetch ?? fetch;
  }

  async health(): Promise<{ status: string }> {
    return this.request("GET", "/healthz");
  }

  async doctor(): Promise<DoctorReport> {
    return this.request("GET", "/v1/doctor");
  }

  async githubAuth(): Promise<GitHubAuthMeta> {
    return this.request("GET", "/v1/auth/github");
  }

  async whoami(): Promise<WhoAmI> {
    return this.request("GET", "/v1/whoami");
  }

  async listOrgs(): Promise<OrgList> {
    return this.request("GET", "/v1/orgs");
  }

  async createOrg(name: string): Promise<Org> {
    return this.request("POST", "/v1/orgs", { name });
  }

  async deleteOrg(name: string): Promise<void> {
    await this.request("DELETE", `/v1/orgs/${encodeURIComponent(name)}`);
  }

  async listOrgMembers(org = this.org || "default"): Promise<OrgMember[]> {
    return this.request("GET", `/v1/orgs/${encodeURIComponent(org)}/members`);
  }

  async addOrgMember(login: string, org = this.org || "default"): Promise<OrgMember> {
    return this.request("POST", `/v1/orgs/${encodeURIComponent(org)}/members`, { login });
  }

  async removeOrgMember(login: string, org = this.org || "default"): Promise<void> {
    await this.request(
      "DELETE",
      `/v1/orgs/${encodeURIComponent(org)}/members/${encodeURIComponent(login)}`,
    );
  }

  async init(): Promise<Project> {
    return this.request("POST", "/v1/init");
  }

  async listProjects(): Promise<Project[]> {
    return this.request("GET", "/v1/projects");
  }

  async listConnectors(): Promise<Connector[]> {
    return this.request("GET", "/v1/connectors");
  }

  async connect(opts: {
    url: string;
    name?: string;
    engine?: string;
    mode?: "physical" | "logical" | string;
    wipe?: boolean;
    dryRun?: boolean;
    tables?: string[];
    onProgress?: ProgressHandler;
  }): Promise<ConnectResult> {
    return this.request(
      "POST",
      `/v1/projects/${this.project}/connect`,
      {
        url: opts.url,
        name: opts.name ?? "primary",
        engine: opts.engine,
        mode: opts.mode,
        wipe: opts.wipe ?? true,
        dry_run: opts.dryRun ?? false,
        tables: opts.tables,
      },
      { progress: true, onProgress: opts.onProgress },
    );
  }

  async deleteConnector(name: string, opts?: { force?: boolean }): Promise<void> {
    const q = opts?.force ? "?force=true" : "";
    await this.request(
      "DELETE",
      `/v1/projects/${this.project}/connectors/${encodeURIComponent(name)}${q}`,
    );
  }

  async suspendConnector(name: string): Promise<{
    connector: Connector;
    branches: BranchRecord[];
    message?: string;
  }> {
    return this.request(
      "POST",
      `/v1/projects/${this.project}/connectors/${encodeURIComponent(name)}/suspend`,
    );
  }

  async resumeConnector(name: string): Promise<{
    connector: Connector;
    branches: BranchRecord[];
    message?: string;
  }> {
    return this.request(
      "POST",
      `/v1/projects/${this.project}/connectors/${encodeURIComponent(name)}/resume`,
    );
  }

  async replication(name?: string): Promise<ReplicationStatus> {
    if (name) {
      return this.request(
        "GET",
        `/v1/projects/${this.project}/connectors/${encodeURIComponent(name)}/replication`,
      );
    }
    return this.request("GET", `/v1/projects/${this.project}/replication`);
  }

  async createBranch(
    name: string,
    from?: string,
    opts?: { onProgress?: ProgressHandler },
  ): Promise<BranchRecord & { psql?: string; mongosh?: string }> {
    const body: Record<string, string> = { name };
    if (from) body.from = from;
    return this.request("POST", `/v1/projects/${this.project}/branches`, body, {
      progress: true,
      onProgress: opts?.onProgress,
    });
  }

  async diffBranch(name: string, from?: string): Promise<BranchDiff> {
    return this.request("GET", this.branchPath(name, "/diff", from));
  }

  async listBranches(): Promise<BranchRecord[]> {
    return this.request("GET", `/v1/projects/${this.project}/branches`);
  }

  async getBranch(name: string, from?: string): Promise<BranchRecord> {
    return this.request("GET", this.branchPath(name, "", from));
  }

  async deleteBranch(name: string, from?: string): Promise<void> {
    await this.request("DELETE", this.branchPath(name, "", from));
  }

  async resetBranch(name: string, from?: string): Promise<BranchRecord> {
    return this.request("POST", this.branchPath(name, "/reset", from));
  }

  async suspendBranch(name: string, from?: string): Promise<BranchRecord> {
    return this.request("POST", this.branchPath(name, "/suspend", from));
  }

  async resumeBranch(name: string, from?: string): Promise<BranchRecord> {
    return this.request("POST", this.branchPath(name, "/resume", from));
  }

  private branchPath(name: string, extra = "", from?: string): string {
    let path = `/v1/projects/${this.project}/branches/${encodeURIComponent(name)}${extra}`;
    if (from) path += `?from=${encodeURIComponent(from)}`;
    return path;
  }

  private headers(extra?: Record<string, string>): Record<string, string> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
      ...extra,
    };
    if (this.org) headers["X-Sprout-Org"] = this.org;
    return headers;
  }

  private async request<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
    opts?: { progress?: boolean; onProgress?: ProgressHandler },
  ): Promise<T> {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
    try {
      const headers = this.headers();
      let payload: string | undefined;
      if (body !== undefined) {
        headers["Content-Type"] = "application/json";
        payload = JSON.stringify(body);
      }
      if (opts?.progress) {
        headers.Accept = "application/x-ndjson";
      }
      const res = await this.fetchImpl(`${this.baseUrl}${path}`, {
        method,
        headers,
        body: payload,
        signal: ctrl.signal,
      });
      if (opts?.progress && (res.headers.get("content-type") || "").includes("ndjson")) {
        return await readNdjson<T>(res, opts.onProgress);
      }
      if (res.status === 204) {
        return undefined as T;
      }
      const text = await res.text();
      let data: unknown = null;
      if (text) {
        try {
          data = JSON.parse(text);
        } catch {
          data = { message: text };
        }
      }
      if (!res.ok) {
        const err = data as { error?: string; message?: string };
        throw new SproutError(
          err.error ?? "http_error",
          err.message ?? (text || res.statusText),
          res.status,
        );
      }
      return data as T;
    } catch (e) {
      if (e instanceof SproutError) throw e;
      if (e instanceof Error && e.name === "AbortError") {
        throw new SproutError("timeout", `request timed out after ${this.timeoutMs}ms`, 0);
      }
      const msg = e instanceof Error ? e.message : String(e);
      throw new SproutError(
        "unreachable",
        `server unreachable (${this.baseUrl}): ${msg}\nStart it with: sprout-server`,
        0,
      );
    } finally {
      clearTimeout(timer);
    }
  }
}

async function readNdjson<T>(res: Response, onProgress?: ProgressHandler): Promise<T> {
  if (!res.body) {
    throw new SproutError("http_error", "empty progress stream", res.status);
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  let result: T | undefined;
  let streamErr: SproutError | undefined;
  while (true) {
    const { done, value } = await reader.read();
    buf += dec.decode(value, { stream: !done });
    const lines = buf.split("\n");
    buf = lines.pop() ?? "";
    for (const line of lines) {
      handleNdjsonLine(line, {
        onProgress,
        status: res.status,
        setResult: (v) => {
          result = v as T;
        },
        setError: (e) => {
          streamErr = e;
        },
      });
    }
    if (done) break;
  }
  if (buf.trim()) {
    handleNdjsonLine(buf, {
      onProgress,
      status: res.status,
      setResult: (v) => {
        result = v as T;
      },
      setError: (e) => {
        streamErr = e;
      },
    });
  }
  if (streamErr) throw streamErr;
  return result as T;
}

function handleNdjsonLine(
  line: string,
  hooks: {
    onProgress?: ProgressHandler;
    status: number;
    setResult: (v: unknown) => void;
    setError: (e: SproutError) => void;
  },
): void {
  const trimmed = line.trim();
  if (!trimmed) return;
  let ev: { type?: string; step?: string; detail?: string; result?: unknown; error?: string; message?: string };
  try {
    ev = JSON.parse(trimmed) as typeof ev;
  } catch {
    hooks.onProgress?.(trimmed);
    return;
  }
  if (ev.type === "step") {
    const msg = [ev.step, ev.detail].filter(Boolean).join(" ").trim();
    if (msg) hooks.onProgress?.(msg);
    return;
  }
  if (ev.type === "result") {
    hooks.setResult(ev.result);
    return;
  }
  if (ev.type === "error") {
    hooks.setError(new SproutError(ev.error ?? "error", ev.message ?? trimmed, hooks.status));
  }
}

function isLoopbackUrl(url: string): boolean {
  try {
    const u = new URL(url);
    return u.hostname === "127.0.0.1" || u.hostname === "localhost" || u.hostname === "::1";
  } catch {
    return false;
  }
}

function resolveToken(token: string | undefined, baseUrl: string): string {
  const t = (token ?? "").trim();
  if (t) return t;
  return isLoopbackUrl(baseUrl) ? "dev-token" : "";
}

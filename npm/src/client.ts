import {
  BranchDiff,
  BranchRecord,
  ConnectResult,
  Connector,
  DoctorReport,
  Project,
  ReplicationStatus,
  SproutClientOptions,
  SproutError,
} from "./types.js";
import { loadConfig } from "./config.js";
import type { GitHubAuthMeta } from "./github.js";

export class SproutClient {
  readonly baseUrl: string;
  readonly token: string;
  readonly project: string;
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
    this.token = opts.token ?? process.env.SPROUT_TOKEN ?? file.token ?? "dev-token";
    this.project = opts.project ?? process.env.SPROUT_PROJECT ?? file.project ?? "default";
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

  async whoami(): Promise<{ kind: string; login: string; id?: number }> {
    return this.request("GET", "/v1/whoami");
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
    mode?: "physical" | "logical" | string;
    wipe?: boolean;
    dryRun?: boolean;
    tables?: string[];
  }): Promise<ConnectResult> {
    return this.request("POST", `/v1/projects/${this.project}/connect`, {
      url: opts.url,
      name: opts.name ?? "primary",
      mode: opts.mode ?? "physical",
      wipe: opts.wipe ?? true,
      dry_run: opts.dryRun ?? false,
      tables: opts.tables,
    });
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

  async createBranch(name: string, from?: string): Promise<BranchRecord & { psql?: string }> {
    const body: Record<string, string> = { name };
    if (from) body.from = from;
    return this.request("POST", `/v1/projects/${this.project}/branches`, body);
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

  private async request<T = unknown>(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<T> {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
    try {
      const headers: Record<string, string> = {
        Authorization: `Bearer ${this.token}`,
      };
      let payload: string | undefined;
      if (body !== undefined) {
        headers["Content-Type"] = "application/json";
        payload = JSON.stringify(body);
      }
      const res = await this.fetchImpl(`${this.baseUrl}${path}`, {
        method,
        headers,
        body: payload,
        signal: ctrl.signal,
      });
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

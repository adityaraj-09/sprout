export type Project = {
  id: string;
  name: string;
  created_at: string;
};

export type BranchRecord = {
  id: string;
  project_id: string;
  name: string;
  role: string;
  status: string;
  port: number;
  data_dir: string;
  snapshot_ref?: string;
  container_id?: string;
  compute?: string;
  connection_string: string;
  psql?: string;
  error_message?: string;
  source_lsn?: string;
  source_connector?: string;
  source_connector_id?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
  last_used_at: string;
};

export type Connector = {
  id: string;
  project_id: string;
  name: string;
  primary_url: string;
  mode: string;
  status: string;
  data_dir: string;
  port: number;
  error_message?: string;
  last_lsn?: string;
  last_lag_bytes: number;
  created_by?: string;
  created_at: string;
  updated_at: string;
};

export type ConnectResult = {
  connector?: Connector;
  lag?: Record<string, unknown>;
  project?: Project;
  connection_string?: string;
  psql?: string;
  dry_run?: boolean;
  estimate?: Record<string, unknown>;
};

export type DoctorCheck = {
  name: string;
  ok: boolean;
  detail: string;
  hint?: string;
  level: string;
};

export type DoctorReport = {
  ok: boolean;
  checks: DoctorCheck[];
};

export type BranchDiff = {
  branch: string;
  parent: string;
  schema: {
    only_on_branch: string[];
    only_on_parent: string[];
    changed_columns: Array<{
      table: string;
      added?: string[];
      removed?: string[];
    }>;
    tables?: Record<string, string[]>;
  };
  rows: Array<{
    table: string;
    branch_rows: number;
    parent_rows: number;
    delta: number;
  }>;
  summary: string;
};

export type ReplicationStatus = {
  connector: Connector;
  lag: Record<string, unknown>;
};

export type SproutClientOptions = {
  /**
   * sprout-server API base URL.
   * Alias of `apiUrl`. Default: flag/opts → env → ~/.sprout/config.json → http://127.0.0.1:8080
   */
  baseUrl?: string;
  /** Preferred name for the control-plane URL (same as `baseUrl`). */
  apiUrl?: string;
  /** Bearer token (opts → env → config; loopback defaults to "dev-token", remote does not) */
  token?: string;
  /** Default project path segment (default "default") */
  project?: string;
  /** Request timeout in ms (default 60 minutes for long connects) */
  timeoutMs?: number;
  /** Skip reading ~/.sprout/config.json */
  ignoreConfigFile?: boolean;
  fetch?: typeof fetch;
};

export class SproutError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = "SproutError";
    this.code = code;
    this.status = status;
  }
}

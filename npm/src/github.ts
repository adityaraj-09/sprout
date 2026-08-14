export type GitHubAuthMeta = {
  enabled: boolean;
  ready?: boolean;
  public?: boolean;
  client_id: string;
  host: string;
  api: string;
  scope: string;
};

type DeviceCode = {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  expires_in: number;
  interval: number;
};

type AccessToken = {
  access_token: string;
  token_type?: string;
  scope?: string;
  error?: string;
  error_description?: string;
};

const UA = "sprout (https://github.com/adityaraj-09/sprout)";

async function postForm(url: string, body: Record<string, string>): Promise<AccessToken & DeviceCode> {
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      Accept: "application/json",
      "User-Agent": UA,
    },
    body: new URLSearchParams(body).toString(),
  });
  if (res.status === 429) {
    return { error: "slow_down", error_description: "rate limited" } as AccessToken & DeviceCode;
  }
  const data = (await res.json()) as AccessToken & DeviceCode;
  if (!res.ok && !data.error) {
    throw new Error(`github_device: HTTP ${res.status}`);
  }
  return data;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(t);
      reject(signal.reason ?? new Error("aborted"));
    });
  });
}

export function browserURL(dc: DeviceCode): string {
  return dc.verification_uri_complete || dc.verification_uri;
}

export async function requestDeviceCode(meta: GitHubAuthMeta): Promise<DeviceCode> {
  const data = await postForm(`${meta.host.replace(/\/$/, "")}/login/device/code`, {
    client_id: meta.client_id,
    scope: meta.scope || "read:user",
  });
  if (data.error) {
    throw new Error(`github_device: ${data.error} (${data.error_description ?? ""})`);
  }
  if (!data.device_code || !data.user_code) {
    throw new Error("github_device: empty device code — enable Device Flow on the OAuth App");
  }
  if (!data.verification_uri) data.verification_uri = `${meta.host.replace(/\/$/, "")}/login/device`;
  if (!data.interval || data.interval < 5) data.interval = 5;
  if (!data.expires_in) data.expires_in = 900;
  return data;
}

export async function waitForToken(
  meta: GitHubAuthMeta,
  dc: DeviceCode,
  signal?: AbortSignal,
): Promise<string> {
  let intervalMs = Math.max(dc.interval, 5) * 1000;
  const deadline = Date.now() + dc.expires_in * 1000;
  const host = meta.host.replace(/\/$/, "");
  while (true) {
    await sleep(intervalMs, signal);
    if (Date.now() > deadline) {
      throw new Error(`github_device: expired_token (open ${dc.verification_uri} and try sprout login again)`);
    }
    const data = await postForm(`${host}/login/oauth/access_token`, {
      client_id: meta.client_id,
      device_code: dc.device_code,
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
    });
    switch (data.error) {
      case undefined:
      case "":
        if (!data.access_token) throw new Error("github_device: empty access token");
        return data.access_token;
      case "authorization_pending":
        continue;
      case "slow_down":
        intervalMs = Math.min(intervalMs + 5000, 30000);
        continue;
      case "expired_token":
        throw new Error("github_device: expired_token (code timed out; run sprout login again)");
      case "access_denied":
        throw new Error("github_device: access_denied (you cancelled in the browser)");
      default:
        throw new Error(`github_device: ${data.error} (${data.error_description ?? "device flow failed"})`);
    }
  }
}

export async function openBrowser(url: string): Promise<void> {
  const { spawn } = await import("node:child_process");
  const platform = process.platform;
  const cmd = platform === "darwin" ? "open" : platform === "win32" ? "cmd" : "xdg-open";
  const args = platform === "win32" ? ["/c", "start", "", url] : [url];
  await new Promise<void>((resolve, reject) => {
    const child = spawn(cmd, args, { detached: true, stdio: "ignore" });
    child.unref();
    child.on("error", reject);
    resolve();
  });
}

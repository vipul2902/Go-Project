// HTTP client for the CloudDeploy API.
//
// Token handling: the access token lives only in memory (never
// localStorage, where injected script could read it). The refresh token is
// an HttpOnly cookie the API sets in "cookie mode"; JavaScript never sees it.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: Record<string, string>,
    readonly requestId?: string,
  ) {
    super(message);
  }
}

const BASE = "/api/v1";
const COOKIE_MODE = { "X-Auth-Mode": "cookie" };

let accessToken: string | null = null;
let onAuthLost: () => void = () => {};

export const auth = {
  token: () => accessToken,
  setToken: (t: string | null) => { accessToken = t; },
  /** Called when the session cannot be recovered (refresh failed). */
  onLost: (fn: () => void) => { onAuthLost = fn; },
};

// Single-flight refresh: when several requests get 401 at once they must
// share ONE refresh. Two refreshes with the same cookie would look like a
// stolen token being reused, and the API would revoke the whole session.
let refreshing: Promise<boolean> | null = null;

export function refreshAccessToken(): Promise<boolean> {
  refreshing ??= (async () => {
    try {
      const res = await fetch(`${BASE}/auth/refresh`, { method: "POST", headers: COOKIE_MODE, credentials: "same-origin" });
      if (!res.ok) return false;
      const body = (await res.json()) as { access_token: string };
      accessToken = body.access_token;
      return true;
    } catch {
      return false;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

/** Authenticated fetch with one transparent refresh-and-retry on 401. */
export async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const send = () => {
    const headers = new Headers(init.headers);
    if (accessToken) headers.set("Authorization", `Bearer ${accessToken}`);
    return fetch(`${BASE}${path}`, { ...init, headers, credentials: "same-origin" });
  };
  let res = await send();
  if (res.status === 401 && (await refreshAccessToken())) {
    res = await send();
  }
  if (res.status === 401) onAuthLost();
  return res;
}

export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { ...opts.headers };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  const res = await authFetch(path, { method: opts.method ?? "GET", headers, body, signal: opts.signal });
  return parse<T>(res);
}

async function parse<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const e = data?.error;
    throw new ApiError(res.status, e?.code ?? "HTTP_ERROR", e?.message ?? res.statusText, e?.details, e?.request_id);
  }
  return data as T;
}

export async function login(email: string, password: string): Promise<void> {
  const res = await fetch(`${BASE}/auth/login`, {
    method: "POST",
    headers: { ...COOKIE_MODE, "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
    credentials: "same-origin",
  });
  const body = await parse<{ access_token: string }>(res);
  accessToken = body.access_token;
}

export async function logout(): Promise<void> {
  try {
    await fetch(`${BASE}/auth/logout`, { method: "POST", headers: COOKIE_MODE, credentials: "same-origin" });
  } finally {
    accessToken = null;
  }
}

/** Human-readable message for an error, including field details. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    const details = err.details ? Object.entries(err.details).map(([k, v]) => `${k}: ${v}`).join("; ") : "";
    return details ? `${err.message} (${details})` : err.message;
  }
  return err instanceof Error ? err.message : "Something went wrong";
}

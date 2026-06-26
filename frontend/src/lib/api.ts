// API client and auth-token handling. Mirrors the original app.js behaviour:
// a Bearer token kept in localStorage, JSON bodies auto-encoded, 401 forces a
// logout. Media/WebSocket endpoints can't set headers, so they take ?token=.

const TOKEN_KEY = "nvr_token";

let token = localStorage.getItem(TOKEN_KEY) || "";
let unauthorizedHandler: (() => void) | null = null;

export function getToken(): string {
  return token;
}

export function isAuthed(): boolean {
  return token !== "";
}

export function setToken(t: string): void {
  token = t;
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}

// onUnauthorized registers the action taken on logout / 401 (set by the router
// to navigate to /login), keeping this module free of a router dependency.
export function onUnauthorized(fn: () => void): void {
  unauthorizedHandler = fn;
}

interface ApiOpts {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
}

export async function api<T = unknown>(path: string, opts: ApiOpts = {}): Promise<T> {
  const headers: Record<string, string> = { ...(opts.headers || {}) };
  if (token) headers["Authorization"] = "Bearer " + token;

  let body: BodyInit | undefined;
  if (opts.body !== undefined && opts.body !== null) {
    if (opts.body instanceof FormData) {
      body = opts.body;
    } else {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }
  }

  const res = await fetch("/api" + path, { method: opts.method, headers, body });
  if (res.status === 401) {
    setToken("");
    unauthorizedHandler?.();
    throw new Error("会话已过期，请重新登录");
  }
  if (res.status === 204) return null as T;

  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data as T;
}

export async function login(username: string, password: string): Promise<void> {
  const data = await api<{ token: string }>("/login", {
    method: "POST",
    body: { username, password },
  });
  setToken(data.token);
}

export function logout(): void {
  setToken("");
  unauthorizedHandler?.();
}

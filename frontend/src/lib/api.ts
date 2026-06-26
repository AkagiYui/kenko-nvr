// API client and auth-token handling. Mirrors the original app.js behaviour:
// a Bearer token kept in localStorage, JSON bodies auto-encoded, 401 forces a
// logout. Media/WebSocket endpoints can't set headers, so they take ?token=.

const TOKEN_KEY = "nvr_token";
const ROLE_KEY = "nvr_role";
const USER_KEY = "nvr_user";

type Role = "admin" | "operator" | "viewer";

let token = localStorage.getItem(TOKEN_KEY) || "";
let role = (localStorage.getItem(ROLE_KEY) as Role | null) ?? "viewer";
let username = localStorage.getItem(USER_KEY) || "";
let unauthorizedHandler: (() => void) | null = null;

export function getToken(): string {
  return token;
}

export function isAuthed(): boolean {
  return token !== "";
}

export function getRole(): Role {
  return role;
}

export function getUsername(): string {
  return username;
}

export function isAdmin(): boolean {
  return role === "admin";
}

export function atLeastOperator(): boolean {
  return role === "admin" || role === "operator";
}

export function setToken(t: string): void {
  token = t;
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}

function setIdentity(r: Role, u: string): void {
  role = r;
  username = u;
  localStorage.setItem(ROLE_KEY, r);
  localStorage.setItem(USER_KEY, u);
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

export async function login(user: string, password: string): Promise<void> {
  const data = await api<{ token: string; username: string; role: Role }>("/login", {
    method: "POST",
    body: { username: user, password },
  });
  setToken(data.token);
  setIdentity(data.role ?? "viewer", data.username ?? user);
}

export function logout(): void {
  // Best-effort server-side revoke; ignore failures (token may already be gone).
  void api("/logout", { method: "POST" }).catch(() => {});
  setToken("");
  setIdentity("viewer", "");
  unauthorizedHandler?.();
}

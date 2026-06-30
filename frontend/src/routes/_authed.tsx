import { createFileRoute, Link, Outlet, redirect, useNavigate } from "@tanstack/solid-router";
import { createSignal, For, onCleanup, onMount, Show } from "solid-js";
import { Icon } from "@iconify-icon/solid";
import { api, getRole, getUsername, isAdmin, isAuthed, logout } from "~/lib/api";
import { getThemePref, setThemePref, type ThemePref } from "~/lib/theme";
import { fmtRate } from "~/lib/format";
import { broadcasterMode, setBroadcasterMode } from "~/lib/broadcaster";
import type { Me, SystemStats } from "~/lib/types";

export const Route = createFileRoute("/_authed")({
  beforeLoad: () => {
    if (!isAuthed()) throw redirect({ to: "/login" });
  },
  component: AuthedLayout,
});

interface NavItem {
  to: string;
  label: string;
  icon: string;
  adminOnly?: boolean;
}

const NAV: NavItem[] = [
  { to: "/dashboard", label: "实时监看", icon: "lucide:monitor-play" },
  { to: "/videowall", label: "电视墙", icon: "lucide:layout-grid" },
  { to: "/cameras", label: "摄像头管理", icon: "lucide:cctv" },
  { to: "/recordings", label: "录像回放", icon: "lucide:clapperboard" },
  { to: "/events", label: "事件", icon: "lucide:radar" },
  { to: "/notifications", label: "通知告警", icon: "lucide:bell", adminOnly: true },
  { to: "/users", label: "用户管理", icon: "lucide:users", adminOnly: true },
  { to: "/settings", label: "系统设置", icon: "lucide:settings", adminOnly: true },
];

const ROLE_LABEL: Record<string, string> = {
  admin: "管理员",
  operator: "操作员",
  viewer: "访客",
};

const THEMES: { value: ThemePref; label: string; icon: string }[] = [
  { value: "light", label: "浅色", icon: "lucide:sun" },
  { value: "dark", label: "深色", icon: "lucide:moon" },
  { value: "system", label: "跟随系统", icon: "lucide:monitor" },
];

function AuthedLayout() {
  const navigate = useNavigate();
  const nav = () => NAV.filter((n) => !n.adminOnly || isAdmin());

  const [theme, setTheme] = createSignal<ThemePref>(getThemePref());
  const pickTheme = (t: ThemePref) => {
    setThemePref(t);
    setTheme(t);
  };

  // Traffic-direction explainer popover.
  const [infoOpen, setInfoOpen] = createSignal(false);
  let trafficRef: HTMLDivElement | undefined;
  onMount(() => {
    const onDocClick = (e: MouseEvent) => {
      if (trafficRef && !trafficRef.contains(e.target as Node)) setInfoOpen(false);
    };
    document.addEventListener("click", onDocClick);
    onCleanup(() => document.removeEventListener("click", onDocClick));
  });

  // Nag the user while the account still uses the built-in default password.
  const [defaultPw, setDefaultPw] = createSignal(false);
  onMount(() => {
    void api<Me>("/me")
      .then((me) => setDefaultPw(!!me.defaultPassword))
      .catch(() => {});
  });

  // Poll system-wide stats for the live traffic / camera widget.
  const [stats, setStats] = createSignal<SystemStats | null>(null);
  onMount(() => {
    let alive = true;
    const tick = async () => {
      try {
        const s = await api<SystemStats>("/stats");
        if (alive) setStats(s);
      } catch {
        /* transient; keep the last value */
      }
    };
    void tick();
    const id = setInterval(tick, 2000);
    onCleanup(() => {
      alive = false;
      clearInterval(id);
    });
  });

  const doLogout = () => {
    logout();
    void navigate({ to: "/login" });
  };

  return (
    <div class="flex h-screen">
      <aside class="w-[210px] shrink-0 bg-base-200 border-r border-base-300 flex flex-col py-4">
        <div class="px-5 pb-4 shrink-0">
          <div class="text-lg font-bold tracking-wide">Kenko NVR</div>
          <div class="text-[11px] text-base-content/50">pure-go network video recorder</div>
        </div>
        <nav class="flex flex-col flex-1 overflow-y-auto">
          <For each={nav()}>
            {(n) => (
              <Link
                to={n.to}
                class="flex items-center gap-2.5 px-5 py-2.5 text-base-content/60 border-l-[3px] border-transparent hover:bg-base-300 hover:text-base-content"
                activeProps={{
                  class:
                    "flex items-center gap-2.5 px-5 py-2.5 text-base-content border-l-[3px] border-primary bg-base-300",
                }}
              >
                <Icon icon={n.icon} width="18" height="18" />
                <span>{n.label}</span>
              </Link>
            )}
          </For>
        </nav>
        <div class="px-5 pt-3 mt-2 border-t border-base-300 flex flex-col gap-3 shrink-0">
          {/* Live system traffic: ingress (下行) + egress (上行). */}
          <div class="relative" ref={trafficRef}>
            <div class="flex items-center justify-between text-[11px] uppercase tracking-wide text-base-content/40 mb-1.5">
              <span class="flex items-center gap-1">
                实时流量
                <button
                  class="leading-none text-base-content/40 hover:text-base-content"
                  aria-label="流量说明"
                  aria-expanded={infoOpen()}
                  onClick={() => setInfoOpen((v) => !v)}
                >
                  <Icon icon="lucide:info" width="13" height="13" />
                </button>
              </span>
              <Show when={stats()}>
                <span class="text-base-content/40">
                  {stats()!.online}/{stats()!.cameras} 在线
                </span>
              </Show>
            </div>
            <div class="flex items-center justify-between gap-2">
              <div class="flex items-center gap-1.5" title="下行（接收）">
                <Icon icon="lucide:arrow-down-to-line" width="15" height="15" class="text-success" />
                <span class="text-[13px] font-semibold tabular-nums">
                  {fmtRate(stats()?.ingressBytesPerSec)}
                </span>
              </div>
              <div class="flex items-center gap-1.5" title="上行（发送）">
                <Icon icon="lucide:arrow-up-from-line" width="15" height="15" class="text-info" />
                <span class="text-[13px] font-semibold tabular-nums">
                  {fmtRate(stats()?.egressBytesPerSec)}
                </span>
              </div>
            </div>

            {/* Popover: what the two directions mean, from the server's view. */}
            <Show when={infoOpen()}>
              <div class="absolute bottom-full left-0 z-20 mb-2 w-[244px] rounded-box border border-base-300 bg-base-100 p-3 text-[12px] leading-relaxed shadow-lg">
                <div class="mb-2 font-semibold text-base-content">流量说明（服务器视角）</div>
                <div class="mb-2 flex gap-2">
                  <Icon
                    icon="lucide:arrow-down-to-line"
                    width="14"
                    height="14"
                    class="mt-0.5 shrink-0 text-success"
                  />
                  <div class="text-base-content/70">
                    <span class="font-medium text-base-content">下行</span>
                    ：服务器<span class="font-medium">接收</span>的数据，主要是从摄像头拉取的视频流。
                  </div>
                </div>
                <div class="flex gap-2">
                  <Icon
                    icon="lucide:arrow-up-from-line"
                    width="14"
                    height="14"
                    class="mt-0.5 shrink-0 text-info"
                  />
                  <div class="text-base-content/70">
                    <span class="font-medium text-base-content">上行</span>
                    ：服务器<span class="font-medium">发送</span>给客户端的数据，例如推送到浏览器的实时画面、录像下载等。
                  </div>
                </div>
              </div>
            </Show>
          </div>

          {/* Theme switcher: light / dark / follow-system. */}
          <div class="join w-full">
            <For each={THEMES}>
              {(t) => (
                <button
                  class="join-item btn btn-xs flex-1 gap-1"
                  classList={{ "btn-primary": theme() === t.value, "btn-ghost": theme() !== t.value }}
                  title={t.label}
                  aria-label={t.label}
                  aria-pressed={theme() === t.value}
                  onClick={() => pickTheme(t.value)}
                >
                  <Icon icon={t.icon} width="14" height="14" />
                </button>
              )}
            </For>
          </div>

          {/* Broadcaster mode: blurs live feeds for screen-sharing / streaming. */}
          <label class="flex items-center justify-between cursor-pointer">
            <span class="flex items-center gap-1.5 text-[12px] text-base-content/60 select-none">
              <Icon icon="lucide:eye-off" width="13" height="13" />
              主播模式
            </span>
            <input
              type="checkbox"
              class="toggle toggle-xs toggle-primary"
              checked={broadcasterMode()}
              onChange={(e) => setBroadcasterMode(e.currentTarget.checked)}
              aria-label="主播模式"
            />
          </label>

          {/* Logged-in user + logout. */}
          <div class="flex items-center justify-between gap-2">
            <Show when={getUsername()}>
              <div class="min-w-0">
                <div class="text-[13px] font-medium truncate">{getUsername()}</div>
                <div class="text-[11px] text-base-content/50">
                  {ROLE_LABEL[getRole()] ?? getRole()}
                </div>
              </div>
            </Show>
            <button
              class="btn btn-ghost btn-sm btn-square shrink-0"
              title="退出登录"
              aria-label="退出登录"
              onClick={doLogout}
            >
              <Icon icon="lucide:log-out" width="16" height="16" />
            </button>
          </div>
        </div>
      </aside>

      <main class="flex-1 overflow-auto p-7">
        <Show when={defaultPw()}>
          <div class="alert alert-warning mb-5">
            <Icon icon="lucide:shield-alert" width="20" height="20" />
            <span>
              当前正在使用默认密码（admin），存在安全风险，请尽快修改。
            </span>
            <Show when={isAdmin()}>
              <Link to="/users" class="btn btn-sm">
                去修改
              </Link>
            </Show>
          </div>
        </Show>
        <Outlet />
      </main>
    </div>
  );
}

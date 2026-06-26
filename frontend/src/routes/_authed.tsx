import { createFileRoute, Link, Outlet, redirect, useNavigate } from "@tanstack/solid-router";
import { For, Show } from "solid-js";
import { Icon } from "@iconify-icon/solid";
import { getRole, getUsername, isAdmin, isAuthed, logout } from "~/lib/api";

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
  { to: "/notifications", label: "通知告警", icon: "lucide:bell", adminOnly: true },
  { to: "/users", label: "用户管理", icon: "lucide:users", adminOnly: true },
  { to: "/settings", label: "系统设置", icon: "lucide:settings", adminOnly: true },
];

const ROLE_LABEL: Record<string, string> = {
  admin: "管理员",
  operator: "操作员",
  viewer: "访客",
};

function AuthedLayout() {
  const navigate = useNavigate();
  const nav = () => NAV.filter((n) => !n.adminOnly || isAdmin());

  const doLogout = () => {
    logout();
    void navigate({ to: "/login" });
  };

  return (
    <div class="flex min-h-screen">
      <aside class="w-[210px] shrink-0 bg-base-200 border-r border-base-300 flex flex-col py-4">
        <div class="px-5 pb-4">
          <div class="text-lg font-bold tracking-wide">Kenko NVR</div>
          <div class="text-[11px] text-base-content/50">pure-go network video recorder</div>
        </div>
        <nav class="flex flex-col">
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
        <div class="flex-1" />
        <div class="px-5">
          <Show when={getUsername()}>
            <div class="text-[12px] text-base-content/50 mb-2">
              {getUsername()} · {ROLE_LABEL[getRole()] ?? getRole()}
            </div>
          </Show>
          <button class="btn btn-ghost btn-sm w-full justify-start gap-2" onClick={doLogout}>
            <Icon icon="lucide:log-out" width="16" height="16" />
            退出登录
          </button>
        </div>
      </aside>

      <main class="flex-1 overflow-auto p-7">
        <Outlet />
      </main>
    </div>
  );
}

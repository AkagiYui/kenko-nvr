import { createFileRoute, Link, Outlet, redirect, useNavigate } from "@tanstack/solid-router";
import { For } from "solid-js";
import { Icon } from "@iconify-icon/solid";
import { isAuthed, logout } from "~/lib/api";

export const Route = createFileRoute("/_authed")({
  beforeLoad: () => {
    if (!isAuthed()) throw redirect({ to: "/login" });
  },
  component: AuthedLayout,
});

const NAV = [
  { to: "/dashboard", label: "实时监看", icon: "lucide:monitor-play" },
  { to: "/cameras", label: "摄像头管理", icon: "lucide:cctv" },
  { to: "/recordings", label: "录像回放", icon: "lucide:clapperboard" },
  { to: "/settings", label: "系统设置", icon: "lucide:settings" },
] as const;

function AuthedLayout() {
  const navigate = useNavigate();

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
          <For each={NAV}>
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

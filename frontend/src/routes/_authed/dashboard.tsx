import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createResource, createSignal, For, onCleanup, onMount, Show } from "solid-js";
import { createStore } from "solid-js/store";
import { api } from "~/lib/api";
import { subscribeStatus } from "~/lib/status";
import { toast } from "~/components/toast";
import { LiveCard } from "~/components/LiveCard";
import type { Camera, CameraStatus } from "~/lib/types";

export const Route = createFileRoute("/_authed/dashboard")({
  component: Dashboard,
});

function Dashboard() {
  const [cameras] = createResource<Camera[]>(() => api("/cameras"));
  const [statuses, setStatuses] = createStore<Record<string, CameraStatus>>({});
  const [seeded, setSeeded] = createSignal(false);

  createEffect(() => {
    if (cameras.error) toast((cameras.error as Error).message, "error");
  });

  // Seed badges/players from the initial camera payload, then keep them updated
  // from the shared status WebSocket.
  createEffect(() => {
    const cams = cameras();
    if (!cams || seeded()) return;
    for (const c of cams) setStatuses(c.id, c.status ?? {});
    setSeeded(true);
  });

  onMount(() => {
    const unsub = subscribeStatus((all) => {
      for (const id in all) setStatuses(id, all[id]);
    });
    onCleanup(unsub);
  });

  return (
    <>
      <h1 class="text-[22px] font-semibold mb-5">实时监看</h1>
      <Show
        when={cameras()}
        fallback={<div class="text-base-content/60">加载中…</div>}
      >
        <Show
          when={cameras()!.length}
          fallback={
            <div class="text-center text-base-content/60 py-16">
              还没有摄像头。前往“摄像头管理”添加一个。
            </div>
          }
        >
          <div
            class="grid gap-4"
            style="grid-template-columns: repeat(auto-fill, minmax(360px, 1fr))"
          >
            <For each={cameras()}>
              {(cam) => <LiveCard camera={cam} status={() => statuses[cam.id] ?? {}} />}
            </For>
          </div>
        </Show>
      </Show>
    </>
  );
}

import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createResource, createSignal, For, Show } from "solid-js";
import { api, atLeastOperator } from "~/lib/api";
import { toast } from "~/components/toast";
import { CameraForm } from "~/components/CameraForm";
import { OnvifDiscover } from "~/components/OnvifDiscover";
import type { Camera, CameraInput } from "~/lib/types";

export const Route = createFileRoute("/_authed/cameras")({
  component: Cameras,
});

interface FormState {
  camera: Camera | null;
  seed?: Partial<CameraInput>;
}

function Cameras() {
  const [cameras, { refetch }] = createResource<Camera[]>(() => api("/cameras"));
  const [form, setForm] = createSignal<FormState | null>(null);
  const [discoverOpen, setDiscoverOpen] = createSignal(false);

  createEffect(() => {
    if (cameras.error) toast((cameras.error as Error).message, "error");
  });

  const del = async (cam: Camera) => {
    if (!confirm(`删除摄像头 “${cam.name}”？其录像记录也会被移除。`)) return;
    try {
      await api(`/cameras/${cam.id}`, { method: "DELETE" });
      toast("已删除");
      void refetch();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <>
      <div class="flex items-center gap-2.5 mb-4 flex-wrap">
        <h1 class="text-[22px] font-semibold m-0">摄像头管理</h1>
        <div class="flex-1" />
        <Show when={atLeastOperator()}>
          <button class="btn btn-ghost btn-sm" onClick={() => setDiscoverOpen(true)}>
            ONVIF 发现
          </button>
          <button class="btn btn-primary btn-sm" onClick={() => setForm({ camera: null })}>
            + 添加摄像头
          </button>
        </Show>
      </div>

      <div class="card bg-base-200 border border-base-300 overflow-x-auto">
        <table class="table">
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>地址</th>
              <th>状态</th>
              <th>录像</th>
              <th>ONVIF</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <Show
              when={cameras() && cameras()!.length}
              fallback={
                <tr>
                  <td colspan="7" class="text-center text-base-content/60 py-8">
                    {cameras.loading ? "加载中…" : "暂无摄像头"}
                  </td>
                </tr>
              }
            >
              <For each={cameras()}>
                {(cam) => (
                  <tr class="hover">
                    <td>{cam.name}</td>
                    <td>{cam.sourceType.toUpperCase()}</td>
                    <td class="text-sm text-base-content/50">
                      {cam.sourceType === "rtmp" ? "推流: /live/" + cam.id : cam.url}
                    </td>
                    <td>
                      <span class="badge badge-ghost">{cam.status?.state ?? "idle"}</span>
                    </td>
                    <td>{cam.record ? "✓" : "—"}</td>
                    <td>{cam.onvifEnabled ? "✓" : "—"}</td>
                    <td class="text-right whitespace-nowrap">
                      <Show when={atLeastOperator()} fallback={<span class="text-base-content/30">—</span>}>
                        <button class="btn btn-ghost btn-xs" onClick={() => setForm({ camera: cam })}>
                          编辑
                        </button>
                        <button class="btn btn-error btn-outline btn-xs ml-1" onClick={() => void del(cam)}>
                          删除
                        </button>
                      </Show>
                    </td>
                  </tr>
                )}
              </For>
            </Show>
          </tbody>
        </table>
      </div>

      <Show when={form()}>
        {(f) => (
          <CameraForm
            camera={f().camera}
            seed={f().seed}
            onClose={() => setForm(null)}
            onSaved={() => {
              setForm(null);
              void refetch();
            }}
          />
        )}
      </Show>

      <Show when={discoverOpen()}>
        <OnvifDiscover
          onClose={() => setDiscoverOpen(false)}
          onAdd={(host) => {
            setDiscoverOpen(false);
            setForm({ camera: null, seed: { sourceType: "onvif", onvifXAddr: host, name: host } });
          }}
        />
      </Show>
    </>
  );
}

import { createEffect, createResource, For, Show } from "solid-js";
import { Modal } from "./Modal";
import { toast } from "./toast";
import { api } from "~/lib/api";
import type { OnvifDevice } from "~/lib/types";

// OnvifDiscover lists ONVIF devices found on the LAN; each can be added as a new
// (ONVIF-source) camera, prefilled with its host.
export function OnvifDiscover(props: { onClose: () => void; onAdd: (host: string) => void }) {
  const [devices] = createResource<OnvifDevice[]>(() => api("/onvif/discover"));

  createEffect(() => {
    if (devices.error) toast((devices.error as Error).message, "error");
  });

  return (
    <Modal title="ONVIF 设备发现" hideOk onClose={props.onClose}>
      <Show
        when={!devices.loading}
        fallback={<p class="text-base-content/60">正在局域网内搜索 ONVIF 设备…</p>}
      >
        <Show
          when={devices()?.length}
          fallback={<p>未发现设备。请确认设备与本机在同一网段。</p>}
        >
          <For each={devices()}>
            {(d) => {
              let host = d.xaddr;
              try {
                host = new URL(d.xaddr).host;
              } catch {
                /* keep raw xaddr */
              }
              return (
                <div class="card bg-base-200 border border-base-300 mb-2">
                  <div class="card-body py-3 px-4 flex-row items-center gap-3">
                    <div class="flex-1 min-w-0">
                      <div class="font-bold">{host}</div>
                      <div class="text-sm text-base-content/50 break-all">{d.xaddr}</div>
                    </div>
                    <button class="btn btn-primary btn-sm" onClick={() => props.onAdd(host)}>
                      添加为摄像头
                    </button>
                  </div>
                </div>
              );
            }}
          </For>
        </Show>
      </Show>
    </Modal>
  );
}

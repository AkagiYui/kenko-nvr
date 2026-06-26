import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createResource, createSignal, For, onCleanup, onMount, Show } from "solid-js";
import { createStore } from "solid-js/store";
import { api } from "~/lib/api";
import { subscribeStatus } from "~/lib/status";
import { isPlaying, startPlayer, stopPlayer, type Overlay } from "~/lib/player";
import type { Camera, CameraStatus, VideoWallConfig } from "~/lib/types";

export const Route = createFileRoute("/_authed/videowall")({
  component: VideoWall,
});

// Supported split layouts (cells = value); the grid is always square.
const SIZES = [1, 4, 9, 16];

function VideoWall() {
  const [cameras] = createResource<Camera[]>(() => api("/cameras"));
  const [statuses, setStatuses] = createStore<Record<string, CameraStatus>>({});
  const [gridSize, setGridSize] = createSignal(4);
  const [layouts, setLayouts] = createStore<Record<string, string[]>>({});
  let wrapRef!: HTMLDivElement;
  let loaded = false;

  onMount(() => {
    void (async () => {
      try {
        const cfg = await api<VideoWallConfig>("/settings/videowall");
        if (cfg?.layouts) {
          for (const k of Object.keys(cfg.layouts)) setLayouts(k, cfg.layouts[k]);
        }
        if (cfg?.gridSize && SIZES.includes(cfg.gridSize)) setGridSize(cfg.gridSize);
      } catch {
        /* fall back to defaults */
      }
      loaded = true;
    })();

    const unsub = subscribeStatus((all) => {
      for (const id in all) setStatuses(id, all[id]);
    });
    onCleanup(unsub);
  });

  // Seed badges from the initial camera payload until the status socket updates.
  createEffect(() => {
    const cams = cameras();
    if (!cams) return;
    for (const c of cams) if (!(c.id in statuses)) setStatuses(c.id, c.status ?? {});
  });

  const cells = () => {
    const size = gridSize();
    const arr = layouts[String(size)] ?? [];
    return Array.from({ length: size }, (_, i) => arr[i] ?? "");
  };

  const save = async () => {
    if (!loaded) return;
    const payload: VideoWallConfig = { gridSize: gridSize(), layouts: { ...layouts } };
    try {
      await api("/settings/videowall", { method: "PUT", body: payload });
    } catch {
      /* a failed save just isn't persisted; the live wall still works */
    }
  };

  const assign = (cell: number, camId: string) => {
    const key = String(gridSize());
    const arr = (layouts[key] ?? []).slice();
    while (arr.length < gridSize()) arr.push("");
    arr[cell] = camId;
    setLayouts(key, arr);
    void save();
  };

  const changeSize = (s: number) => {
    setGridSize(s);
    void save();
  };

  const cols = () => Math.sqrt(gridSize());
  const camById = (id: string) => cameras()?.find((c) => c.id === id);

  const toggleFullscreen = () => {
    if (document.fullscreenElement) {
      void document.exitFullscreen();
    } else {
      void wrapRef?.requestFullscreen?.();
    }
  };

  return (
    <div>
      <div class="flex items-center gap-3 mb-4 flex-wrap">
        <h1 class="text-[22px] font-semibold">电视墙</h1>
        <div class="join">
          <For each={SIZES}>
            {(s) => (
              <button
                class="btn btn-sm join-item"
                classList={{ "btn-primary": gridSize() === s, "btn-ghost": gridSize() !== s }}
                onClick={() => changeSize(s)}
              >
                {s === 1 ? "单画面" : `${s} 分屏`}
              </button>
            )}
          </For>
        </div>
        <div class="flex-1" />
        <button class="btn btn-sm btn-ghost" onClick={toggleFullscreen}>
          全屏
        </button>
      </div>

      <div ref={wrapRef} class="bg-base-300/30 rounded-lg p-2">
        <div
          class="grid gap-2"
          style={`grid-template-columns: repeat(${cols()}, minmax(0, 1fr));`}
        >
          <For each={cells()}>
            {(camId, i) => (
              <WallTile
                camera={camById(camId)}
                status={() => (camId ? statuses[camId] ?? {} : {})}
                cameras={cameras() ?? []}
                onAssign={(id) => assign(i(), id)}
              />
            )}
          </For>
        </div>
      </div>

      <p class="text-xs text-base-content/40 mt-3">
        点击空格选择摄像头；悬停画面可切换或移除。布局自动保存。
      </p>
    </div>
  );
}

function WallTile(props: {
  camera?: Camera;
  status: () => CameraStatus;
  cameras: Camera[];
  onAssign: (id: string) => void;
}) {
  let videoRef!: HTMLVideoElement;
  const [ov, setOv] = createSignal<string | null>(null);
  let startedFor: string | null = null;
  const overlay: Overlay = {
    show: (t) => setOv(t),
    prompt: (t) => setOv(t),
    hide: () => setOv(null),
  };

  createEffect(() => {
    const cam = props.camera;
    const st = props.status();
    if (!cam) {
      if (startedFor) {
        stopPlayer(videoRef);
        startedFor = null;
      }
      setOv(null);
      return;
    }
    const playing = isPlaying(videoRef);
    if (st.live) {
      if (!playing || startedFor !== cam.id) {
        stopPlayer(videoRef);
        setOv("加载中…");
        startPlayer(videoRef, cam.id, overlay, "mse");
        startedFor = cam.id;
      }
    } else {
      if (playing) {
        stopPlayer(videoRef);
        startedFor = null;
      }
      setOv(st.error ? "无信号" : "等待视频流…");
    }
  });

  onCleanup(() => stopPlayer(videoRef));

  return (
    <div class="relative bg-black rounded overflow-hidden aspect-video group">
      <video ref={videoRef} class="w-full h-full object-contain bg-black" muted playsinline autoplay />

      <Show when={!props.camera}>
        <div class="absolute inset-0 flex items-center justify-center p-2">
          <select
            class="select select-bordered select-sm max-w-full"
            onChange={(e) => props.onAssign(e.currentTarget.value)}
          >
            <option value="">+ 选择摄像头</option>
            <For each={props.cameras}>{(c) => <option value={c.id}>{c.name}</option>}</For>
          </select>
        </div>
      </Show>

      <Show when={props.camera}>
        <Show when={ov() !== null}>
          <div class="absolute inset-0 flex items-center justify-center text-xs text-white/60 pointer-events-none">
            {ov()}
          </div>
        </Show>

        <div class="absolute top-0 inset-x-0 flex items-center gap-1 px-2 py-1 bg-gradient-to-b from-black/70 to-transparent opacity-0 group-hover:opacity-100 transition">
          <span class="text-xs text-white truncate flex-1">{props.camera!.name}</span>
          <Show when={props.status().motion}>
            <span class="badge badge-warning badge-xs">移动</span>
          </Show>
          <select
            class="select select-bordered select-xs"
            value=""
            onChange={(e) => props.onAssign(e.currentTarget.value)}
          >
            <option value="">切换…</option>
            <For each={props.cameras}>{(c) => <option value={c.id}>{c.name}</option>}</For>
          </select>
          <button
            class="btn btn-xs btn-ghost text-white"
            title="移除"
            onClick={() => props.onAssign("")}
          >
            ✕
          </button>
        </div>

        <div class="absolute bottom-0 inset-x-0 px-2 py-1 bg-gradient-to-t from-black/60 to-transparent group-hover:opacity-0 transition">
          <span class="text-xs text-white/90 truncate">{props.camera!.name}</span>
        </div>
      </Show>
    </div>
  );
}

import { createEffect, createSignal, onCleanup, Show } from "solid-js";
import type { Camera, CameraStatus } from "~/lib/types";
import { isPlaying, startPlayer, stopPlayer, type Overlay } from "~/lib/player";
import { PtzControls } from "./PtzControls";

const STATE_TEXT: Record<string, string> = {
  running: "在线",
  connecting: "连接中",
  error: "错误",
  idle: "空闲",
};

function badgeClass(state?: string): string {
  switch (state) {
    case "running":
      return "badge-success";
    case "connecting":
      return "badge-warning";
    case "error":
      return "badge-error";
    default:
      return "badge-ghost";
  }
}

// LiveCard renders one camera tile and reconciles its player with live status:
// it starts the player when the camera goes live and stops it when it drops, so
// the grid self-heals across reconnects (matching the original behaviour).
export function LiveCard(props: { camera: Camera; status: () => CameraStatus }) {
  let videoRef!: HTMLVideoElement;
  const [ovText, setOvText] = createSignal<string | null>("加载中…");
  const [ovClick, setOvClick] = createSignal<(() => void) | null>(null);

  const overlay: Overlay = {
    show: (t) => {
      setOvClick(null);
      setOvText(t);
    },
    prompt: (t, onClick) => {
      setOvText(t);
      setOvClick(() => onClick);
    },
    hide: () => {
      setOvClick(null);
      setOvText(null);
    },
  };

  createEffect(() => {
    const st = props.status();
    const playing = isPlaying(videoRef);
    if (st.live && !playing) {
      overlay.show("加载中…");
      startPlayer(videoRef, props.camera.id, overlay);
    } else if (!st.live && playing) {
      stopPlayer(videoRef);
      overlay.show(st.error ? "无信号：" + st.error : "等待视频流…");
    } else if (!st.live && !playing && ovText() === "加载中…") {
      overlay.show(st.error ? "无信号：" + st.error : "等待视频流…");
    }
  });

  onCleanup(() => stopPlayer(videoRef));

  const badgeText = () => {
    const st = props.status();
    let t = STATE_TEXT[st.state ?? ""] ?? st.state ?? "…";
    if (st.recording) t += " · 录像中";
    return t;
  };

  return (
    <div class="card bg-base-200 border border-base-300 overflow-hidden">
      <div class="flex items-center gap-2 px-4 py-3 border-b border-base-300">
        <span class="font-semibold flex-1 truncate">{props.camera.name}</span>
        <span class={`badge ${badgeClass(props.status().state)}`} title={props.status().error ?? ""}>
          {badgeText()}
        </span>
      </div>

      <div class="relative bg-black aspect-video">
        <video
          ref={videoRef}
          class="w-full h-full object-contain bg-black"
          muted
          playsinline
          autoplay
        />
        <Show when={ovText() !== null}>
          <div
            class="absolute inset-0 flex items-center justify-center text-sm text-base-content/60 text-center p-4"
            classList={{ "cursor-pointer": !!ovClick() }}
            onClick={() => ovClick()?.()}
          >
            {ovText()}
          </div>
        </Show>
      </div>

      <Show when={props.camera.onvifEnabled}>
        <div class="p-4">
          <PtzControls cameraId={props.camera.id} />
        </div>
      </Show>
    </div>
  );
}

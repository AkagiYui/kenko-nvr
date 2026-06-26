import { createEffect, createSignal, onCleanup, Show } from "solid-js";
import type { Camera, CameraStatus } from "~/lib/types";
import { atLeastOperator, getToken } from "~/lib/api";
import { isPlaying, startPlayer, stopPlayer, type LiveMode, type Overlay } from "~/lib/player";
import { startTalk, type TalkHandle } from "~/lib/talk";
import { toast } from "./toast";
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

// LiveCard renders one camera tile: it reconciles its player with live status,
// offers an MSE/WebRTC toggle, two-way talk, and external pull URLs.
export function LiveCard(props: { camera: Camera; status: () => CameraStatus }) {
  let videoRef!: HTMLVideoElement;
  const [ovText, setOvText] = createSignal<string | null>("加载中…");
  const [ovClick, setOvClick] = createSignal<(() => void) | null>(null);
  const [mode, setMode] = createSignal<LiveMode>("mse");
  const [talking, setTalking] = createSignal(false);
  let talk: TalkHandle | null = null;
  let startedMode: LiveMode | null = null;

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
    const m = mode();
    const playing = isPlaying(videoRef);
    if (st.live) {
      if (!playing || startedMode !== m) {
        stopPlayer(videoRef);
        overlay.show("加载中…");
        startPlayer(videoRef, props.camera.id, overlay, m);
        startedMode = m;
      }
    } else if (playing) {
      stopPlayer(videoRef);
      startedMode = null;
      overlay.show(st.error ? "无信号：" + st.error : "等待视频流…");
    } else if (ovText() === "加载中…") {
      overlay.show(st.error ? "无信号：" + st.error : "等待视频流…");
    }
  });

  const stopTalk = () => {
    talk?.stop();
    talk = null;
    setTalking(false);
  };

  const toggleTalk = () => {
    if (talking()) {
      stopTalk();
      return;
    }
    setTalking(true);
    talk = startTalk(props.camera.id, (status, message) => {
      if (status === "error") {
        toast(message ?? "对讲失败", "error");
        stopTalk();
      } else if (status === "ready") {
        toast("对讲已开启，正在说话…");
      }
    });
  };

  onCleanup(() => {
    stopPlayer(videoRef);
    stopTalk();
  });

  const badgeText = () => {
    const st = props.status();
    let t = STATE_TEXT[st.state ?? ""] ?? st.state ?? "…";
    if (st.recording) t += " · 录像中";
    return t;
  };

  const canTalk = () => atLeastOperator() && props.camera.sourceType !== "rtmp";
  const host = () => location.hostname;
  const tok = () => encodeURIComponent(getToken());
  const httpBase = () => `${location.protocol}//${location.host}/api/cameras/${props.camera.id}`;

  return (
    <div class="card bg-base-200 border border-base-300 overflow-hidden">
      <div class="flex items-center gap-2 px-4 py-3 border-b border-base-300">
        <span class="font-semibold flex-1 truncate">{props.camera.name}</span>
        <Show when={props.status().motion}>
          <span class="badge badge-warning gap-1">移动</span>
        </Show>
        <span class={`badge ${badgeClass(props.status().state)}`} title={props.status().error ?? ""}>
          {badgeText()}
        </span>
      </div>

      <div class="relative bg-black aspect-video">
        <video ref={videoRef} class="w-full h-full object-contain bg-black" muted playsinline autoplay />
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

      <div class="flex items-center gap-2 px-3 py-2 border-t border-base-300">
        <div class="join">
          <button
            class="btn btn-xs join-item"
            classList={{ "btn-primary": mode() === "mse", "btn-ghost": mode() !== "mse" }}
            onClick={() => setMode("mse")}
          >
            MSE
          </button>
          <button
            class="btn btn-xs join-item"
            classList={{ "btn-primary": mode() === "webrtc", "btn-ghost": mode() !== "webrtc" }}
            onClick={() => setMode("webrtc")}
          >
            WebRTC
          </button>
        </div>
        <Show when={canTalk()}>
          <button
            class="btn btn-xs gap-1"
            classList={{ "btn-error": talking(), "btn-ghost": !talking() }}
            onClick={toggleTalk}
          >
            {talking() ? "停止对讲" : "🎙 对讲"}
          </button>
        </Show>
        <div class="flex-1" />
      </div>

      <Show when={props.camera.onvifEnabled}>
        <div class="px-4 pb-3">
          <PtzControls cameraId={props.camera.id} />
        </div>
      </Show>

      <details class="px-4 pb-3 text-xs">
        <summary class="cursor-pointer text-base-content/50">外部拉流地址</summary>
        <div class="mt-2 space-y-1 break-all text-base-content/70">
          <div>RTSP：<code>rtsp://{host()}:8554/{props.camera.id}</code></div>
          <div>HTTP-FLV：<code>{httpBase()}/flv?token={tok()}</code></div>
          <div>HTTP-TS：<code>{httpBase()}/ts?token={tok()}</code></div>
        </div>
      </details>
    </div>
  );
}

// Shared, persisted live-view transport/format selection, used by both the
// monitor (LiveCard) and the videowall so one choice governs all live tiles.
//
//   - mse:    fMP4 over WebSocket into MSE — the default; low latency, H.264
//             (the source is transcoded from H.265 on demand).
//   - webrtc: WHEP — the lowest latency, H.264.
//   - hls:    HLS — most compatible (plays H.264/H.265 natively), higher latency.

import { createSignal } from "solid-js";
import type { LiveMode } from "./player";

export type { LiveMode };

const KEY = "nvr_live_mode";

function load(): LiveMode {
  const v = localStorage.getItem(KEY);
  return v === "webrtc" || v === "hls" ? v : "mse";
}

const [liveMode, setSignal] = createSignal<LiveMode>(load());

export { liveMode };

export function setLiveMode(m: LiveMode): void {
  localStorage.setItem(KEY, m);
  setSignal(m);
}

// LIVE_MODES drives the selector UI.
export const LIVE_MODES: { value: LiveMode; label: string; hint: string }[] = [
  { value: "mse", label: "MSE", hint: "默认 · 低延迟（H.264）" },
  { value: "webrtc", label: "WebRTC", hint: "最低延迟（H.264）" },
  { value: "hls", label: "HLS", hint: "兼容性最好 · 延迟较高" },
];

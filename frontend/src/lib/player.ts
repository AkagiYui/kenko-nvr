// Live player — single-connection fMP4-over-WebSocket into MSE (HLS fallback).
//
// Ported faithfully from the original app.js. The grid streams like a real
// low-latency live site: ONE persistent WebSocket per camera carrying an fMP4
// init segment then one fragment per GOP, appended to a MediaSource. When MSE or
// the codec isn't available (e.g. iOS Safari) it falls back to HLS. In every
// path we muted-autoplay, with a click-to-play prompt if the browser blocks it.
//
// The DOM-overlay calls of the original are replaced by an injected Overlay so
// the logic stays identical but renders through Solid signals in LiveCard.

import Hls from "hls.js";
import { getToken } from "./api";

export interface Overlay {
  /** Show a non-interactive status message. */
  show(text: string): void;
  /** Show a clickable prompt (used for click-to-play when autoplay is blocked). */
  prompt(text: string, onClick: () => void): void;
  /** Hide the overlay. */
  hide(): void;
}

export interface PlayerHandle {
  destroy(): void;
}

const players = new Map<HTMLVideoElement, PlayerHandle>();

export function isPlaying(video: HTMLVideoElement): boolean {
  return players.has(video);
}

export function stopPlayer(video: HTMLVideoElement): void {
  const p = players.get(video);
  if (p) {
    p.destroy();
    players.delete(video);
  }
  video.removeAttribute("src");
  if (video.load) video.load();
}

export function stopAllPlayers(): void {
  for (const v of [...players.keys()]) stopPlayer(v);
}

// tryPlay performs the muted autoplay; if the browser blocks it (no user gesture
// yet) we surface a click-to-play prompt rather than a frozen tile.
function tryPlay(video: HTMLVideoElement, overlay: Overlay): void {
  const p = video.play();
  if (p && p.catch) {
    p.catch(() => {
      overlay.prompt("▶ 点击播放", () => {
        video.play().then(() => overlay.hide()).catch(() => {});
      });
    });
  }
}

export type LiveMode = "mse" | "webrtc";

export function startPlayer(
  video: HTMLVideoElement,
  cameraId: string,
  overlay: Overlay,
  mode: LiveMode = "mse",
): void {
  video.muted = true;
  video.playsInline = true;
  if (mode === "webrtc" && window.RTCPeerConnection) {
    startWebrtcPlayer(video, cameraId, overlay);
  } else if (window.MediaSource) {
    startMsePlayer(video, cameraId, overlay);
  } else {
    startHlsPlayer(video, cameraId, overlay);
  }
}

// startWebrtcPlayer negotiates a WHEP session and renders the remote track. It
// is the lowest-latency path; the server sends H.264 video (no audio over
// WebRTC in this version).
function startWebrtcPlayer(video: HTMLVideoElement, cameraId: string, overlay: Overlay): void {
  const pc = new RTCPeerConnection({ iceServers: [] });
  let stopped = false;
  pc.addTransceiver("video", { direction: "recvonly" });
  pc.ontrack = (ev) => {
    video.srcObject = ev.streams[0];
    tryPlay(video, overlay);
  };
  pc.onconnectionstatechange = () => {
    if (["failed", "disconnected", "closed"].includes(pc.connectionState) && !stopped) {
      overlay.show("WebRTC 连接中断");
    }
  };

  void (async () => {
    try {
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await waitIceGathering(pc);
      const url = `/api/cameras/${cameraId}/webrtc?token=${encodeURIComponent(getToken())}`;
      const res = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/sdp" },
        body: pc.localDescription?.sdp ?? "",
      });
      if (!res.ok) {
        overlay.show("WebRTC 不可用，回退中…");
        pc.close();
        if (!stopped) startMsePlayer(video, cameraId, overlay);
        return;
      }
      const answer = await res.text();
      await pc.setRemoteDescription({ type: "answer", sdp: answer });
    } catch {
      pc.close();
      if (!stopped) startMsePlayer(video, cameraId, overlay);
    }
  })();

  players.set(video, {
    destroy() {
      stopped = true;
      try {
        pc.close();
      } catch {
        /* ignore */
      }
      video.srcObject = null;
    },
  });
}

function waitIceGathering(pc: RTCPeerConnection): Promise<void> {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const check = () => {
      if (pc.iceGatheringState === "complete") {
        pc.removeEventListener("icegatheringstatechange", check);
        resolve();
      }
    };
    pc.addEventListener("icegatheringstatechange", check);
    setTimeout(resolve, 2000); // don't wait forever for ICE
  });
}

function startMsePlayer(video: HTMLVideoElement, cameraId: string, overlay: Overlay): void {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const url = `${proto}://${location.host}/api/cameras/${cameraId}/mse?token=${encodeURIComponent(getToken())}`;
  const START_BUFFER = 2; // seconds buffered before playback starts
  const MAX_BUFFER = 30; // seconds of back-buffer to retain behind playback

  let ms: MediaSource | null = null;
  let sb: SourceBuffer | null = null;
  let ws: WebSocket | null = null;
  let queue: Uint8Array<ArrayBuffer>[] = [];
  let mime: string | null = null;
  let initSeg: Uint8Array<ArrayBuffer> | null = null;
  let stopped = false;
  let started = false;
  let recovering = false;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let delay = 1000;
  let recoverAt = 0;
  let recoverBurst = 0;

  const closeSocket = () => {
    if (ws) {
      try {
        ws.onclose = ws.onmessage = ws.onerror = null;
        ws.close();
      } catch {
        /* ignore */
      }
      ws = null;
    }
  };
  const fallback = () => {
    stopped = true;
    closeSocket();
    startHlsPlayer(video, cameraId, overlay);
  };
  const fullReconnect = () => {
    closeSocket();
    sb = null;
    queue = [];
    started = false;
    if (stopped) return;
    overlay.show("重连中…");
    retry = setTimeout(connect, delay);
    delay = Math.min(delay * 2, 8000);
  };

  const pump = () => {
    if (!sb || sb.updating || !queue.length) return;
    try {
      sb.appendBuffer(queue.shift()!);
    } catch (e) {
      if (e && (e as Error).name === "QuotaExceededError") trim(true);
    }
  };
  const trim = (hard: boolean) => {
    if (!sb || sb.updating || !sb.buffered.length) return;
    const start = sb.buffered.start(0);
    const end = sb.buffered.end(sb.buffered.length - 1);
    let cut = video.currentTime - (hard ? 2 : MAX_BUFFER);
    cut = Math.max(cut, end - (MAX_BUFFER + 5));
    if (cut > start + 1) {
      try {
        sb.remove(start, cut);
      } catch {
        /* ignore */
      }
    }
  };
  const onUpdateEnd = () => {
    trim(false);
    pump();
    maybeStart();
  };

  const maybeStart = () => {
    if (started || !sb || !sb.buffered.length) return;
    const begin = sb.buffered.start(0);
    const end = sb.buffered.end(sb.buffered.length - 1);
    if (end - begin < START_BUFFER) return;
    if (video.currentTime < begin || video.currentTime > end) {
      try {
        video.currentTime = begin;
      } catch {
        /* ignore */
      }
    }
    started = true;
    tryPlay(video, overlay);
  };

  const recover = () => {
    if (stopped || recovering || !initSeg || !mime) return;
    const now = Date.now();
    recoverBurst = now - recoverAt < 2500 ? recoverBurst + 1 : 0;
    recoverAt = now;
    if (recoverBurst >= 4) {
      recoverBurst = 0;
      fullReconnect();
      return;
    }
    recovering = true;
    started = false;
    sb = null;
    queue = [];
    try {
      if (ms && ms.readyState === "open") ms.endOfStream();
    } catch {
      /* ignore */
    }
    ms = new MediaSource();
    video.src = URL.createObjectURL(ms);
    ms.addEventListener(
      "sourceopen",
      () => {
        URL.revokeObjectURL(video.src);
        try {
          sb = ms!.addSourceBuffer(mime!);
          sb.mode = "segments";
          sb.addEventListener("updateend", onUpdateEnd);
          queue.push(initSeg!);
          pump();
        } catch {
          recovering = false;
          fullReconnect();
          return;
        }
        recovering = false;
      },
      { once: true },
    );
  };

  const connect = () => {
    if (stopped) return;
    ms = new MediaSource();
    video.src = URL.createObjectURL(ms);
    ms.addEventListener(
      "sourceopen",
      () => {
        URL.revokeObjectURL(video.src);
        ws = new WebSocket(url);
        ws.binaryType = "arraybuffer";
        ws.onmessage = (ev) => {
          if (typeof ev.data === "string") {
            try {
              mime = JSON.parse(ev.data).mimeCodec || "";
            } catch {
              /* ignore */
            }
            if (!mime || !MediaSource.isTypeSupported(mime)) return fallback();
            try {
              sb = ms!.addSourceBuffer(mime);
              sb.mode = "segments";
              sb.addEventListener("updateend", onUpdateEnd);
            } catch {
              return fallback();
            }
            delay = 1000; // connected cleanly
          } else {
            const chunk = new Uint8Array(ev.data as ArrayBuffer);
            if (initSeg === null) initSeg = chunk; // first binary message = init
            queue.push(chunk);
            pump();
          }
        };
        ws.onerror = () => {
          try {
            ws!.close();
          } catch {
            /* ignore */
          }
        };
        ws.onclose = () => fullReconnect();
      },
      { once: true },
    );
  };

  video.addEventListener("playing", () => overlay.hide());
  video.addEventListener("error", () => {
    if (video.error && video.error.code === 3) recover(); // MEDIA_ERR_DECODE
    else fullReconnect();
  });
  const keeper = setInterval(() => {
    if (!sb || !sb.buffered.length || video.seeking) return;
    const end = sb.buffered.end(sb.buffered.length - 1);
    if (started && video.readyState >= 2 && video.paused) tryPlay(video, overlay);
    if (end - video.currentTime > 8) {
      try {
        video.currentTime = end - 1;
      } catch {
        /* ignore */
      }
    }
  }, 3000);

  connect();
  players.set(video, {
    destroy() {
      stopped = true;
      clearInterval(keeper);
      if (retry) clearTimeout(retry);
      closeSocket();
      try {
        if (ms && ms.readyState === "open") ms.endOfStream();
      } catch {
        /* ignore */
      }
      video.removeAttribute("src");
      if (video.load) video.load();
    },
  });
}

function startHlsPlayer(video: HTMLVideoElement, cameraId: string, overlay: Overlay): void {
  const url = `/api/cameras/${cameraId}/hls/index.m3u8`;
  if (Hls.isSupported()) {
    const hls = new Hls({
      lowLatencyMode: false,
      liveSyncDurationCount: 2,
      backBufferLength: 30,
      maxLiveSyncPlaybackRate: 1.5,
      xhrSetup: (xhr) => {
        const t = getToken();
        if (t) xhr.setRequestHeader("Authorization", "Bearer " + t);
      },
    });
    hls.on(Hls.Events.MANIFEST_PARSED, () => tryPlay(video, overlay));
    hls.on(Hls.Events.ERROR, (_e, data) => {
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        overlay.show("重连中…");
        hls.startLoad();
      } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
        hls.recoverMediaError();
      } else {
        overlay.show("播放错误：" + (data.details || data.type));
        hls.destroy();
      }
    });
    video.addEventListener("playing", () => overlay.hide());
    hls.loadSource(url);
    hls.attachMedia(video);
    players.set(video, {
      destroy() {
        try {
          hls.destroy();
        } catch {
          /* ignore */
        }
      },
    });
  } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
    video.src = url + "?token=" + encodeURIComponent(getToken());
    video.addEventListener("loadedmetadata", () => tryPlay(video, overlay));
    video.addEventListener("playing", () => overlay.hide());
    video.addEventListener("error", () => overlay.show("播放错误"));
    players.set(video, {
      destroy() {
        video.removeAttribute("src");
        if (video.load) video.load();
      },
    });
  } else {
    overlay.show("此浏览器无法在网页中播放该视频流");
  }
}

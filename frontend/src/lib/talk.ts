// Two-way audio: capture the microphone, downsample to 8 kHz mono PCM16 and
// stream it to the camera's back channel over a WebSocket. Requires a secure
// context (HTTPS or localhost) for getUserMedia.

import { getToken } from "./api";

export interface TalkHandle {
  stop(): void;
}

export type TalkStatus = "connecting" | "ready" | "error";

export function startTalk(
  cameraId: string,
  onStatus: (status: TalkStatus, message?: string) => void,
): TalkHandle {
  let stopped = false;
  let stream: MediaStream | null = null;
  let ctx: AudioContext | null = null;
  let node: ScriptProcessorNode | null = null;

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(
    `${proto}://${location.host}/api/cameras/${cameraId}/talk?token=${encodeURIComponent(getToken())}`,
  );
  ws.binaryType = "arraybuffer";
  onStatus("connecting");

  const cleanup = () => {
    stopped = true;
    if (node) {
      node.disconnect();
      node.onaudioprocess = null;
      node = null;
    }
    if (ctx) {
      void ctx.close();
      ctx = null;
    }
    if (stream) {
      stream.getTracks().forEach((t) => t.stop());
      stream = null;
    }
    try {
      ws.close();
    } catch {
      /* ignore */
    }
  };

  ws.onmessage = (ev) => {
    if (typeof ev.data !== "string") return;
    try {
      const msg = JSON.parse(ev.data) as { error?: string; status?: string };
      if (msg.error) {
        onStatus("error", msg.error);
        cleanup();
      }
    } catch {
      /* ignore */
    }
  };
  ws.onerror = () => {
    onStatus("error", "连接失败");
  };
  ws.onclose = () => {
    if (!stopped) onStatus("error", "连接已关闭");
  };

  ws.onopen = () => {
    void startCapture();
  };

  async function startCapture() {
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true },
      });
    } catch {
      onStatus("error", "无法访问麦克风（需 HTTPS 或 localhost）");
      cleanup();
      return;
    }
    if (stopped) {
      stream.getTracks().forEach((t) => t.stop());
      return;
    }
    const Ctx = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    ctx = new Ctx();
    const source = ctx.createMediaStreamSource(stream);
    node = ctx.createScriptProcessor(2048, 1, 1);
    node.onaudioprocess = (e: AudioProcessingEvent) => {
      if (stopped || ws.readyState !== WebSocket.OPEN) return;
      const input = e.inputBuffer.getChannelData(0);
      const pcm = downsampleToPCM16(input, ctx!.sampleRate, 8000);
      if (pcm.byteLength > 0) ws.send(pcm);
    };
    source.connect(node);
    node.connect(ctx.destination);
    onStatus("ready");
  }

  return { stop: cleanup };
}

// downsampleToPCM16 linearly resamples Float32 [-1,1] mono audio to 8 kHz and
// packs it as little-endian signed 16-bit PCM.
function downsampleToPCM16(input: Float32Array, inRate: number, outRate: number): ArrayBuffer {
  if (outRate >= inRate) {
    // No downsample needed (or upsampling, which we avoid): pack as-is.
    return packPCM16(input);
  }
  const ratio = inRate / outRate;
  const outLen = Math.floor(input.length / ratio);
  const out = new Float32Array(outLen);
  for (let i = 0; i < outLen; i++) {
    out[i] = input[Math.floor(i * ratio)];
  }
  return packPCM16(out);
}

function packPCM16(samples: Float32Array): ArrayBuffer {
  const buf = new ArrayBuffer(samples.length * 2);
  const view = new DataView(buf);
  for (let i = 0; i < samples.length; i++) {
    let s = samples[i];
    if (s > 1) s = 1;
    else if (s < -1) s = -1;
    view.setInt16(i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return buf;
}

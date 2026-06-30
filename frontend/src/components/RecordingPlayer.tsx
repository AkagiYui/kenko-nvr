// Recording playback via ArtPlayer with a user-selectable format:
//   - "H.264 兼容 (转码)": on-demand HLS (hls.js) — plays in every browser,
//     loads only segments near the playhead, and retries on a network blip
//     (resumable). Jump-to-offset is baked into the transcode start (?from).
//   - "原始 (快)": the original MP4 over HTTP range — zero server CPU, but needs
//     the browser to support the recorded codec (often H.265).
// The choice is remembered in localStorage.

import Artplayer from "artplayer";
import Hls from "hls.js";
import { createEffect, onCleanup } from "solid-js";
import { api, getToken } from "~/lib/api";

export type RecFormat = "hls" | "original";

const FORMAT_KEY = "nvr_rec_format";

export function getRecFormat(): RecFormat {
  return localStorage.getItem(FORMAT_KEY) === "original" ? "original" : "hls";
}
function setRecFormat(f: RecFormat) {
  localStorage.setItem(FORMAT_KEY, f);
}

interface Props {
  recordingId: string;
  // Seek target in seconds (e.g. a person's appearance offset).
  offsetSec?: number;
  // Called when the clip finishes (used to chain event clips).
  onEnded?: () => void;
}

export function RecordingPlayer(props: Props) {
  let container!: HTMLDivElement;
  let art: Artplayer | undefined;
  let token = 0; // guards against races between async rebuilds

  const fileUrl = () =>
    `/api/recordings/${props.recordingId}/file?token=${encodeURIComponent(getToken())}`;

  const playHls = (video: HTMLMediaElement, url: string) => {
    if (Hls.isSupported()) {
      const hls = new Hls({
        xhrSetup: (xhr) => {
          const t = getToken();
          if (t) xhr.setRequestHeader("Authorization", "Bearer " + t);
        },
      });
      hls.loadSource(url);
      hls.attachMedia(video as HTMLVideoElement);
      art?.on("destroy", () => hls.destroy());
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = `${url}?token=${encodeURIComponent(getToken())}`;
    }
  };

  const build = async (fmt: RecFormat) => {
    const mine = ++token;
    if (art) {
      art.destroy(true);
      art = undefined;
    }

    let url = fileUrl();
    let type = "mp4";
    if (fmt === "hls") {
      try {
        const r = await api<{ playlist: string }>(
          `/recordings/${props.recordingId}/hls?from=${Math.max(0, Math.floor(props.offsetSec ?? 0))}`,
        );
        url = r.playlist;
        type = "m3u8";
      } catch {
        // Transcode unavailable (e.g. cloud-only clip): fall back to the original.
        url = fileUrl();
        type = "mp4";
      }
    }
    if (mine !== token) return; // a newer rebuild superseded this one

    art = new Artplayer({
      container,
      url,
      type,
      autoplay: true,
      pip: true,
      setting: true,
      playbackRate: true,
      fullscreen: true,
      miniProgressBar: true,
      theme: "#7c5cff",
      customType: { m3u8: (video: HTMLMediaElement, u: string) => playHls(video, u) },
      settings: [
        {
          html: "格式",
          tooltip: fmt === "hls" ? "H.264 兼容" : "原始",
          selector: [
            { html: "H.264 兼容（转码）", value: "hls", default: fmt === "hls" },
            { html: "原始（快，需浏览器支持）", value: "original", default: fmt === "original" },
          ],
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          onSelect(item: any) {
            const v: RecFormat = item.value === "original" ? "original" : "hls";
            setRecFormat(v);
            void build(v);
            return item.html;
          },
        },
      ],
    });

    if (props.onEnded) {
      const onEnded = props.onEnded;
      art.on("video:ended", () => onEnded());
    }

    // For the original MP4 the timeline is the whole file, so seek to the offset;
    // for HLS the transcode already started at the offset (playhead 0 = offset).
    if (type === "mp4" && (props.offsetSec ?? 0) > 0) {
      const a = art;
      a.once("ready", () => {
        try {
          a.currentTime = props.offsetSec!;
        } catch {
          /* ignore */
        }
      });
    }
  };

  createEffect(() => {
    // Rebuild when the recording changes; the format is read once per build.
    void props.recordingId;
    void build(getRecFormat());
  });

  onCleanup(() => {
    token++;
    if (art) art.destroy(true);
  });

  return <div ref={container} class="w-full aspect-video bg-black rounded-lg overflow-hidden" />;
}

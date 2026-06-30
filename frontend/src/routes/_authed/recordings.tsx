import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createMemo, createResource, createSignal, For, Show } from "solid-js";
import { api, getToken } from "~/lib/api";
import { toast } from "~/components/toast";
import { Modal } from "~/components/Modal";
import { Timeline } from "~/components/Timeline";
import { RangePicker } from "~/components/RangePicker";
import { defaultRange } from "~/lib/timerange";
import { fmtDur, fmtSize, fmtTime } from "~/lib/format";
import type { Camera, NvrEvent, Recording } from "~/lib/types";

export const Route = createFileRoute("/_authed/recordings")({
  component: Recordings,
});

interface Playing {
  rec: Recording;
  offset: number;
}

function Recordings() {
  const [cameras] = createResource<Camera[]>(() => api("/cameras"));
  const [camId, setCamId] = createSignal("");
  const init = defaultRange();
  const [from, setFrom] = createSignal(init.from);
  const [to, setTo] = createSignal(init.to);
  const [playing, setPlaying] = createSignal<Playing | null>(null);

  // Shared query string for the recordings + events (timeline) fetches.
  const query = createMemo(() => {
    const p = new URLSearchParams({ limit: "1000", from: String(from()), to: String(to()) });
    if (camId()) p.set("cameraId", camId());
    return p.toString();
  });

  const [recs, { refetch }] = createResource(query, (qs) => api<Recording[]>(`/recordings?${qs}`));
  const [events, { refetch: refetchEvents }] = createResource(query, (qs) =>
    api<NvrEvent[]>(`/events?${qs}`),
  );

  const camName = createMemo(() => {
    const m: Record<string, string> = {};
    for (const c of cameras() ?? []) m[c.id] = c.name;
    return m;
  });

  createEffect(() => {
    if (recs.error) toast((recs.error as Error).message, "error");
  });

  const del = async (r: Recording) => {
    if (!confirm("删除此录像文件？")) return;
    try {
      await api(`/recordings/${r.id}`, { method: "DELETE" });
      toast("已删除");
      void refetch();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const fileUrl = (r: Recording, download = false) =>
    `/api/recordings/${r.id}/file?${download ? "download=1&" : ""}token=${encodeURIComponent(getToken())}`;

  return (
    <>
      <h1 class="text-[22px] font-semibold mb-5">录像回放</h1>

      <div class="flex items-center gap-2.5 mb-4 flex-wrap">
        <select
          class="select select-bordered select-sm max-w-[240px]"
          value={camId()}
          onChange={(e) => setCamId(e.currentTarget.value)}
        >
          <option value="">全部摄像头</option>
          <For each={cameras()}>{(c) => <option value={c.id}>{c.name}</option>}</For>
        </select>
        <RangePicker from={from()} to={to()} onChange={(f, t) => { setFrom(f); setTo(t); }} />
        <button
          class="btn btn-ghost btn-sm"
          onClick={() => {
            void refetch();
            void refetchEvents();
          }}
        >
          刷新
        </button>
      </div>

      <div class="card bg-base-200 border border-base-300 p-4 mb-4">
        <Timeline
          start={from()}
          end={to()}
          recordings={recs() ?? []}
          events={events() ?? []}
          onSeek={(rec, offset) => setPlaying({ rec, offset })}
        />
      </div>

      <div class="card bg-base-200 border border-base-300 overflow-x-auto">
        <table class="table">
          <thead>
            <tr>
              <th>摄像头</th>
              <th>开始时间</th>
              <th>时长</th>
              <th>大小</th>
              <th>S3</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <Show
              when={recs() && recs()!.length}
              fallback={
                <tr>
                  <td colspan="6" class="text-center text-base-content/60 py-8">
                    {recs.loading ? "加载中…" : "当天暂无录像"}
                  </td>
                </tr>
              }
            >
              <For each={recs()}>
                {(r) => (
                  <tr class="hover">
                    <td>{camName()[r.cameraId] ?? r.cameraId}</td>
                    <td>{fmtTime(r.startTime)}</td>
                    <td>{fmtDur(r.durationMs)}</td>
                    <td>{fmtSize(r.sizeBytes)}</td>
                    <td>
                      <div class="flex items-center gap-1">
                        <Show when={r.localRemoved} fallback={
                          <Show when={r.uploaded} fallback="—">
                            <span class="badge badge-success">已上传</span>
                          </Show>
                        }>
                          <span class="badge badge-info" title="本地文件已按规则删除，从 S3 流式回放">
                            仅云端
                          </span>
                        </Show>
                        <Show when={r.encrypted}>
                          <span class="badge badge-ghost" title="S3 上为加密存储，回放时自动解密">
                            🔒
                          </span>
                        </Show>
                      </div>
                    </td>
                    <td class="text-right whitespace-nowrap">
                      <button
                        class="btn btn-ghost btn-xs"
                        disabled={!r.complete}
                        onClick={() => setPlaying({ rec: r, offset: 0 })}
                      >
                        播放
                      </button>
                      <a class="btn btn-ghost btn-xs ml-1" href={fileUrl(r, true)}>
                        下载
                      </a>
                      <button class="btn btn-error btn-outline btn-xs ml-1" onClick={() => void del(r)}>
                        删除
                      </button>
                    </td>
                  </tr>
                )}
              </For>
            </Show>
          </tbody>
        </table>
      </div>

      <Show when={playing()}>
        {(p) => (
          <Modal title="录像回放" hideOk width={760} onClose={() => setPlaying(null)}>
            <video
              controls
              autoplay
              class="w-full bg-black rounded-lg"
              src={fileUrl(p().rec)}
              onLoadedMetadata={(e) => {
                if (p().offset > 0) {
                  try {
                    e.currentTarget.currentTime = p().offset;
                  } catch {
                    /* ignore */
                  }
                }
              }}
            />
            <p class="text-sm text-base-content/60 mt-2">
              {(camName()[p().rec.cameraId] ?? "") + " · " + fmtTime(p().rec.startTime)}
            </p>
          </Modal>
        )}
      </Show>
    </>
  );
}

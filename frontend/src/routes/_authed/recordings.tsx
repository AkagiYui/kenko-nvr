import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createMemo, createResource, createSignal, For, Show } from "solid-js";
import { api, getToken } from "~/lib/api";
import { toast } from "~/components/toast";
import { Modal } from "~/components/Modal";
import { fmtDur, fmtSize, fmtTime } from "~/lib/format";
import type { Camera, Recording } from "~/lib/types";

export const Route = createFileRoute("/_authed/recordings")({
  component: Recordings,
});

function Recordings() {
  const [cameras] = createResource<Camera[]>(() => api("/cameras"));
  const [camId, setCamId] = createSignal("");
  const [recs, { refetch }] = createResource(
    () => ({ id: camId() }),
    ({ id }) => api<Recording[]>("/recordings?limit=500" + (id ? "&cameraId=" + id : "")),
  );
  const [playing, setPlaying] = createSignal<Recording | null>(null);

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

      <div class="flex items-center gap-2.5 mb-4">
        <select
          class="select select-bordered select-sm max-w-[240px]"
          value={camId()}
          onChange={(e) => setCamId(e.currentTarget.value)}
        >
          <option value="">全部摄像头</option>
          <For each={cameras()}>{(c) => <option value={c.id}>{c.name}</option>}</For>
        </select>
        <button class="btn btn-ghost btn-sm" onClick={() => void refetch()}>
          刷新
        </button>
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
                    {recs.loading ? "加载中…" : "暂无录像"}
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
                      <Show when={r.uploaded} fallback="—">
                        <span class="badge badge-success">已上传</span>
                      </Show>
                    </td>
                    <td class="text-right whitespace-nowrap">
                      <button
                        class="btn btn-ghost btn-xs"
                        disabled={!r.complete}
                        onClick={() => setPlaying(r)}
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
        {(r) => (
          <Modal title="录像回放" hideOk width={760} onClose={() => setPlaying(null)}>
            <video
              controls
              autoplay
              class="w-full bg-black rounded-lg"
              src={fileUrl(r())}
            />
            <p class="text-sm text-base-content/60 mt-2">
              {(camName()[r().cameraId] ?? "") + " · " + fmtTime(r().startTime)}
            </p>
          </Modal>
        )}
      </Show>
    </>
  );
}

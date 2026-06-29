import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createMemo, createResource, createSignal, For, Show } from "solid-js";
import { Icon } from "@iconify-icon/solid";
import { api, getToken } from "~/lib/api";
import { toast } from "~/components/toast";
import { Modal } from "~/components/Modal";
import { MultiSelect } from "~/components/MultiSelect";
import { RangePicker } from "~/components/RangePicker";
import { defaultRange } from "~/lib/timerange";
import { fmtDur, fmtTime } from "~/lib/format";
import type { Camera, NvrEvent, Recording } from "~/lib/types";

export const Route = createFileRoute("/_authed/events")({
  component: Events,
});

const TYPE_LABEL: Record<string, string> = {
  motion: "移动侦测",
};

// Known event types for the type filter (currently only motion detection).
const EVENT_TYPES = [{ value: "motion", label: "移动侦测" }];

function Events() {
  const [cameras] = createResource<Camera[]>(() => api("/cameras"));
  const [camIds, setCamIds] = createSignal<string[]>([]);
  const [types, setTypes] = createSignal<string[]>([]);
  const init = defaultRange();
  const [from, setFrom] = createSignal(init.from);
  const [to, setTo] = createSignal(init.to);
  const [playing, setPlaying] = createSignal<NvrEvent | null>(null);

  const query = createMemo(() => {
    const p = new URLSearchParams({
      withRecordings: "1",
      limit: "1000",
      from: String(from()),
      to: String(to()),
    });
    for (const c of camIds()) p.append("cameraId", c);
    for (const t of types()) p.append("type", t);
    return p.toString();
  });

  const [events, { refetch }] = createResource(query, (qs) => api<NvrEvent[]>(`/events?${qs}`));

  const camOptions = createMemo(() => (cameras() ?? []).map((c) => ({ value: c.id, label: c.name })));
  const camName = createMemo(() => {
    const m: Record<string, string> = {};
    for (const c of cameras() ?? []) m[c.id] = c.name;
    return m;
  });

  createEffect(() => {
    if (events.error) toast((events.error as Error).message, "error");
  });

  const eventDur = (e: NvrEvent) => (e.endTime ? e.endTime - e.startTime : undefined);

  return (
    <>
      <h1 class="text-[22px] font-semibold mb-5">事件</h1>

      <div class="flex items-center gap-2.5 mb-4 flex-wrap">
        <MultiSelect
          options={camOptions()}
          selected={camIds()}
          onChange={setCamIds}
          placeholder="全部摄像头"
          width={200}
        />
        <MultiSelect
          options={EVENT_TYPES}
          selected={types()}
          onChange={setTypes}
          placeholder="全部类型"
          width={150}
        />
        <RangePicker from={from()} to={to()} onChange={(f, t) => { setFrom(f); setTo(t); }} />
        <button class="btn btn-ghost btn-sm" onClick={() => void refetch()}>
          刷新
        </button>
      </div>

      <div class="card bg-base-200 border border-base-300 overflow-x-auto">
        <table class="table">
          <thead>
            <tr>
              <th>摄像头</th>
              <th>类型</th>
              <th>开始时间</th>
              <th>持续</th>
              <th>关联录像</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <Show
              when={events() && events()!.length}
              fallback={
                <tr>
                  <td colspan="6" class="text-center text-base-content/60 py-8">
                    {events.loading ? "加载中…" : "当天暂无事件"}
                  </td>
                </tr>
              }
            >
              <For each={events()}>
                {(e) => {
                  const recs = () => e.recordings ?? [];
                  return (
                    <tr class="hover">
                      <td>{camName()[e.cameraId] ?? e.cameraId}</td>
                      <td>
                        <span class="badge badge-warning gap-1">
                          <Icon icon="lucide:radar" width="13" height="13" />
                          {TYPE_LABEL[e.type] ?? e.type}
                        </span>
                      </td>
                      <td>{fmtTime(e.startTime)}</td>
                      <td>{e.endTime ? fmtDur(eventDur(e)) : <span class="text-info">进行中</span>}</td>
                      <td>
                        <Show
                          when={recs().length}
                          fallback={<span class="text-base-content/40">—</span>}
                        >
                          <span class="inline-flex items-center gap-1 text-base-content/70">
                            <Icon icon="lucide:film" width="14" height="14" />
                            {recs().length} 段
                          </span>
                        </Show>
                      </td>
                      <td class="text-right whitespace-nowrap">
                        <button
                          class="btn btn-ghost btn-xs gap-1"
                          disabled={!recs().length}
                          onClick={() => setPlaying(e)}
                        >
                          <Icon icon="lucide:play" width="13" height="13" />
                          播放
                        </button>
                      </td>
                    </tr>
                  );
                }}
              </For>
            </Show>
          </tbody>
        </table>
      </div>

      <Show when={playing()}>
        {(e) => (
          <EventPlayer
            event={e()}
            cameraName={camName()[e().cameraId] ?? e().cameraId}
            onClose={() => setPlaying(null)}
          />
        )}
      </Show>
    </>
  );
}

// EventPlayer plays the clips that cover an event, in order. The first clip is
// seeked to the moment the event began; when a clip ends it advances to the
// next, so a motion event spanning a segment boundary plays through seamlessly.
function EventPlayer(props: { event: NvrEvent; cameraName: string; onClose: () => void }) {
  const recs = props.event.recordings ?? [];
  const [idx, setIdx] = createSignal(0);
  const current = () => recs[idx()];

  const fileUrl = (r: Recording) =>
    `/api/recordings/${r.id}/file?token=${encodeURIComponent(getToken())}`;

  // Offset (seconds) into a clip where the event begins; only the first clip is
  // seeked, and only when the event started after the clip did.
  const startOffset = (r: Recording) => Math.max(0, (props.event.startTime - r.startTime) / 1000);

  return (
    <Modal title="事件录像" hideOk width={760} onClose={props.onClose}>
      <video
        controls
        autoplay
        class="w-full bg-black rounded-lg"
        src={fileUrl(current())}
        onLoadedMetadata={(ev) => {
          // Land on the event moment in the first clip; later clips play from 0.
          const off = idx() === 0 ? startOffset(current()) : 0;
          if (off > 0) {
            try {
              ev.currentTarget.currentTime = off;
            } catch {
              /* ignore */
            }
          }
        }}
        onEnded={() => {
          if (idx() < recs.length - 1) setIdx(idx() + 1);
        }}
      />
      <div class="mt-2 flex items-center justify-between text-sm text-base-content/60">
        <span>
          {props.cameraName} · {fmtTime(props.event.startTime)}
        </span>
        <Show when={recs.length > 1}>
          <span>
            第 {idx() + 1} / {recs.length} 段
          </span>
        </Show>
      </div>

      <Show when={recs.length > 1}>
        <div class="mt-2 flex flex-col gap-1">
          <For each={recs}>
            {(r, i) => (
              <button
                class="flex items-center justify-between rounded-field px-3 py-1.5 text-sm hover:bg-base-300"
                classList={{ "bg-base-300 font-medium": i() === idx() }}
                onClick={() => setIdx(i())}
              >
                <span class="inline-flex items-center gap-2">
                  <Icon
                    icon={i() === idx() ? "lucide:play" : "lucide:film"}
                    width="13"
                    height="13"
                    class={i() === idx() ? "text-primary" : "text-base-content/50"}
                  />
                  第 {i() + 1} 段 · {fmtTime(r.startTime)}
                </span>
                <span class="text-base-content/50">{fmtDur(r.durationMs)}</span>
              </button>
            )}
          </For>
        </div>
      </Show>
    </Modal>
  );
}

import { createMemo, For } from "solid-js";
import type { NvrEvent, Recording } from "~/lib/types";

interface Props {
  start: number; // window start, epoch ms
  end: number; // window end, epoch ms
  recordings: Recording[];
  events: NvrEvent[];
  // onSeek is called with the recording covering the clicked instant and the
  // offset (seconds) into that file.
  onSeek: (rec: Recording, offsetSec: number) => void;
}

const TICKS = 6; // number of label intervals across the bar

// Timeline draws the selected time window as a bar: recording coverage as blocks
// and motion events as ticks. Clicking a covered moment seeks into the matching
// recording. The window can span anything from minutes to weeks.
export function Timeline(props: Props) {
  const span = () => Math.max(props.end - props.start, 1);

  const blocks = createMemo(() => {
    const s0 = props.start;
    const e0 = props.end;
    const total = span();
    return props.recordings
      .map((r) => {
        const s = new Date(r.startTime).getTime();
        const e = s + (r.durationMs ?? 0);
        return { r, s: Math.max(s, s0), e: Math.min(e || s, e0) };
      })
      .filter((b) => b.e > b.s)
      .map((b) => ({
        rec: b.r,
        left: ((b.s - s0) / total) * 100,
        width: Math.max(((b.e - b.s) / total) * 100, 0.25),
      }));
  });

  const marks = createMemo(() => {
    const s0 = props.start;
    const total = span();
    return props.events
      .map((ev) => ({ ev, left: ((new Date(ev.startTime).getTime() - s0) / total) * 100 }))
      .filter((m) => m.left >= 0 && m.left <= 100);
  });

  // Adaptive tick labels: show clock time for short windows, dates for long ones.
  const fmtTick = (t: number) => {
    const d = new Date(t);
    const p = (n: number) => String(n).padStart(2, "0");
    if (span() <= 36 * 60 * 60 * 1000) return `${p(d.getHours())}:${p(d.getMinutes())}`;
    return `${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  };
  const ticks = createMemo(() =>
    Array.from({ length: TICKS + 1 }, (_, i) => fmtTick(props.start + (span() * i) / TICKS)),
  );

  const handleClick = (e: MouseEvent & { currentTarget: HTMLDivElement }) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const ratio = Math.min(Math.max((e.clientX - rect.left) / rect.width, 0), 1);
    const t = props.start + ratio * span();
    for (const r of props.recordings) {
      const s = new Date(r.startTime).getTime();
      const en = s + (r.durationMs ?? 0);
      if (t >= s && t <= en) {
        props.onSeek(r, Math.max(0, (t - s) / 1000));
        return;
      }
    }
  };

  return (
    <div class="select-none">
      <div
        class="relative h-12 cursor-pointer overflow-hidden rounded-md bg-base-300"
        onClick={handleClick}
        title="点击带录像的时段跳转播放"
      >
        <For each={blocks()}>
          {(b) => (
            <div
              class="absolute top-0 bottom-0 bg-primary/70 hover:bg-primary"
              style={`left:${b.left}%;width:${b.width}%`}
              title={new Date(b.rec.startTime).toLocaleString()}
            />
          )}
        </For>
        <For each={marks()}>
          {(m) => (
            <div
              class="absolute top-0 bottom-0 w-[2px] bg-warning"
              style={`left:${m.left}%`}
              title={"移动 " + new Date(m.ev.startTime).toLocaleString()}
            />
          )}
        </For>
      </div>
      <div class="mt-1 flex justify-between px-0.5 text-[10px] text-base-content/40">
        <For each={ticks()}>{(t) => <span>{t}</span>}</For>
      </div>
      <div class="mt-1 flex gap-4 text-xs text-base-content/50">
        <span class="flex items-center gap-1">
          <span class="inline-block h-3 w-3 rounded-sm bg-primary/70" /> 录像
        </span>
        <span class="flex items-center gap-1">
          <span class="inline-block h-3 w-[2px] bg-warning" /> 移动事件
        </span>
      </div>
    </div>
  );
}

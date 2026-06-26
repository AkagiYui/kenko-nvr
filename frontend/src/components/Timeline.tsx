import { createMemo, For } from "solid-js";
import type { NvrEvent, Recording } from "~/lib/types";

const DAY_MS = 24 * 60 * 60 * 1000;

interface Props {
  date: Date; // local day (any time within it)
  recordings: Recording[];
  events: NvrEvent[];
  // onSeek is called with the recording covering the clicked instant and the
  // offset (seconds) into that file.
  onSeek: (rec: Recording, offsetSec: number) => void;
}

// Timeline draws a 24-hour bar: recording coverage as blocks and motion events
// as ticks. Clicking a covered moment seeks into the matching recording.
export function Timeline(props: Props) {
  const dayStart = createMemo(() => {
    const d = props.date;
    return new Date(d.getFullYear(), d.getMonth(), d.getDate(), 0, 0, 0, 0).getTime();
  });

  const blocks = createMemo(() => {
    const start0 = dayStart();
    const end0 = start0 + DAY_MS;
    return props.recordings
      .map((r) => {
        const s = new Date(r.startTime).getTime();
        const e = s + (r.durationMs ?? 0);
        return { r, s: Math.max(s, start0), e: Math.min(e || s, end0) };
      })
      .filter((b) => b.e > b.s)
      .map((b) => ({
        rec: b.r,
        left: ((b.s - start0) / DAY_MS) * 100,
        width: Math.max(((b.e - b.s) / DAY_MS) * 100, 0.25),
      }));
  });

  const marks = createMemo(() => {
    const start0 = dayStart();
    return props.events
      .map((ev) => ({ ev, left: ((new Date(ev.startTime).getTime() - start0) / DAY_MS) * 100 }))
      .filter((m) => m.left >= 0 && m.left <= 100);
  });

  const handleClick = (e: MouseEvent & { currentTarget: HTMLDivElement }) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const ratio = Math.min(Math.max((e.clientX - rect.left) / rect.width, 0), 1);
    const t = dayStart() + ratio * DAY_MS;
    // Find the recording covering t.
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
        class="relative h-12 bg-base-300 rounded-md overflow-hidden cursor-pointer"
        onClick={handleClick}
        title="点击带录像的时段跳转播放"
      >
        <For each={blocks()}>
          {(b) => (
            <div
              class="absolute top-0 bottom-0 bg-primary/70 hover:bg-primary"
              style={`left:${b.left}%;width:${b.width}%`}
              title={new Date(b.rec.startTime).toLocaleTimeString()}
            />
          )}
        </For>
        <For each={marks()}>
          {(m) => (
            <div
              class="absolute top-0 bottom-0 w-[2px] bg-warning"
              style={`left:${m.left}%`}
              title={"移动 " + new Date(m.ev.startTime).toLocaleTimeString()}
            />
          )}
        </For>
      </div>
      <div class="flex justify-between text-[10px] text-base-content/40 mt-1 px-0.5">
        <For each={[0, 3, 6, 9, 12, 15, 18, 21, 24]}>
          {(h) => <span>{String(h).padStart(2, "0")}</span>}
        </For>
      </div>
      <div class="flex gap-4 text-xs text-base-content/50 mt-1">
        <span class="flex items-center gap-1"><span class="inline-block w-3 h-3 bg-primary/70 rounded-sm" /> 录像</span>
        <span class="flex items-center gap-1"><span class="inline-block w-[2px] h-3 bg-warning" /> 移动事件</span>
      </div>
    </div>
  );
}

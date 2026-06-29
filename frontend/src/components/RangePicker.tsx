import { For } from "solid-js";
import { fromLocalInput, RANGE_PRESETS, toLocalInput } from "~/lib/timerange";

interface Props {
  from: number;
  to: number;
  onChange: (from: number, to: number) => void;
}

// RangePicker edits an arbitrary [from, to] window via two datetime-local inputs
// plus quick presets. Times are local; values are epoch ms.
export function RangePicker(props: Props) {
  return (
    <div class="flex flex-wrap items-center gap-2">
      <input
        type="datetime-local"
        class="input input-bordered input-sm"
        value={toLocalInput(props.from)}
        onChange={(e) => {
          const v = fromLocalInput(e.currentTarget.value);
          if (v) props.onChange(v, props.to);
        }}
      />
      <span class="text-base-content/40">至</span>
      <input
        type="datetime-local"
        class="input input-bordered input-sm"
        value={toLocalInput(props.to)}
        onChange={(e) => {
          const v = fromLocalInput(e.currentTarget.value);
          if (v) props.onChange(props.from, v);
        }}
      />
      <div class="join">
        <For each={RANGE_PRESETS}>
          {(p) => (
            <button
              type="button"
              class="btn join-item btn-ghost btn-sm"
              onClick={() => {
                const r = p.range();
                props.onChange(r.from, r.to);
              }}
            >
              {p.label}
            </button>
          )}
        </For>
      </div>
    </div>
  );
}

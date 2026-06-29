import { createSignal, For, onCleanup, onMount, Show } from "solid-js";
import { Icon } from "@iconify-icon/solid";

export interface Option {
  value: string;
  label: string;
}

interface Props {
  options: Option[];
  selected: string[];
  onChange: (values: string[]) => void;
  // Shown on the trigger when nothing is selected (i.e. "any"), e.g. "全部摄像头".
  placeholder: string;
  width?: number;
}

// MultiSelect is a checkbox dropdown for OR-filtering by several values. An empty
// selection means "any" (the placeholder is shown). Closes on outside click.
export function MultiSelect(props: Props) {
  const [open, setOpen] = createSignal(false);
  let ref: HTMLDivElement | undefined;

  onMount(() => {
    const onDoc = (e: MouseEvent) => {
      if (ref && !ref.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("click", onDoc);
    onCleanup(() => document.removeEventListener("click", onDoc));
  });

  const toggle = (v: string) => {
    const set = new Set(props.selected);
    if (set.has(v)) set.delete(v);
    else set.add(v);
    props.onChange([...set]);
  };

  const summary = () => {
    if (props.selected.length === 0) return props.placeholder;
    if (props.selected.length === 1) {
      const o = props.options.find((o) => o.value === props.selected[0]);
      return o?.label ?? props.selected[0];
    }
    return `已选 ${props.selected.length} 项`;
  };

  return (
    <div class="relative" ref={ref} style={props.width ? `width:${props.width}px` : undefined}>
      <button
        type="button"
        class="btn btn-sm w-full justify-between gap-2 border-base-content/20 bg-base-100 font-normal hover:bg-base-200"
        aria-haspopup="listbox"
        aria-expanded={open()}
        onClick={() => setOpen((v) => !v)}
      >
        <span class="truncate" classList={{ "text-base-content/50": props.selected.length === 0 }}>
          {summary()}
        </span>
        <Icon icon="lucide:chevron-down" width="14" height="14" class="shrink-0 opacity-60" />
      </button>

      <Show when={open()}>
        <div class="absolute left-0 z-30 mt-1 max-h-[300px] w-full min-w-[180px] overflow-auto rounded-box border border-base-300 bg-base-100 p-1 shadow-lg">
          <Show when={props.selected.length}>
            <button
              type="button"
              class="w-full px-2 py-1 text-left text-xs text-base-content/60 hover:text-base-content"
              onClick={() => props.onChange([])}
            >
              清除选择
            </button>
          </Show>
          <For each={props.options}>
            {(o) => (
              <label class="flex cursor-pointer items-center gap-2 rounded-field px-2 py-1.5 hover:bg-base-200">
                <input
                  type="checkbox"
                  class="checkbox checkbox-xs"
                  checked={props.selected.includes(o.value)}
                  onChange={() => toggle(o.value)}
                />
                <span class="truncate text-sm">{o.label}</span>
              </label>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}

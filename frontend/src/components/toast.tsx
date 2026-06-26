import { createSignal, For } from "solid-js";

type Kind = "ok" | "error";

interface ToastItem {
  id: number;
  msg: string;
  kind: Kind;
}

const [items, setItems] = createSignal<ToastItem[]>([]);
let seq = 0;

// toast shows a transient message, auto-dismissed after 4s (as in the original).
export function toast(msg: string, kind: Kind = "ok"): void {
  const id = ++seq;
  setItems((list) => [...list, { id, msg, kind }]);
  setTimeout(() => setItems((list) => list.filter((t) => t.id !== id)), 4000);
}

export function Toaster() {
  return (
    <div class="toast toast-end z-[100]">
      <For each={items()}>
        {(t) => (
          <div class={`alert ${t.kind === "error" ? "alert-error" : "alert-success"} shadow-lg`}>
            <span>{t.msg}</span>
          </div>
        )}
      </For>
    </div>
  );
}

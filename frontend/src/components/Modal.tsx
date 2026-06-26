import { createSignal, type JSX, Show } from "solid-js";
import { toast } from "./toast";

interface ModalProps {
  title: string;
  okLabel?: string;
  /** Hide the OK button (for read-only dialogs). */
  hideOk?: boolean;
  /** Max width in px; defaults to daisyUI's modal-box width. */
  width?: number;
  /** Return false to keep the modal open (e.g. validation failed). */
  onOk?: () => Promise<boolean | void> | boolean | void;
  onClose: () => void;
  children: JSX.Element;
}

// Modal replicates the original modal({title, body, onOk}) behaviour: a centred
// dialog whose OK runs an async handler, surfaces errors as a toast, and stays
// open if the handler throws or returns false.
export function Modal(props: ModalProps) {
  const [busy, setBusy] = createSignal(false);

  const handleOk = async () => {
    setBusy(true);
    try {
      const keep = await props.onOk?.();
      if (keep !== false) props.onClose();
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      class="modal modal-open"
      onClick={(e) => {
        if (e.target === e.currentTarget) props.onClose();
      }}
    >
      <div
        class="modal-box"
        style={
          props.width
            ? `width:${props.width}px;max-width:calc(100vw - 2rem)`
            : undefined
        }
      >
        <h3 class="text-lg font-semibold mb-4">{props.title}</h3>
        <div>{props.children}</div>
        <div class="modal-action">
          <button class="btn btn-ghost" onClick={() => props.onClose()}>
            {props.hideOk ? "关闭" : "取消"}
          </button>
          <Show when={!props.hideOk}>
            <button class="btn btn-primary" disabled={busy()} onClick={handleOk}>
              {props.okLabel ?? "保存"}
            </button>
          </Show>
        </div>
      </div>
    </div>
  );
}

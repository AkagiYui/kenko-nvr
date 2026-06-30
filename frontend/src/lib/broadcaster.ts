import { createSignal } from "solid-js";

const LS_KEY = "kenko:broadcaster-mode";

const [broadcasterMode, setBroadcasterModeRaw] = createSignal(
  typeof localStorage !== "undefined" && localStorage.getItem(LS_KEY) === "true",
);

export const broadcasterMode_ = broadcasterMode;

export function setBroadcasterMode(val: boolean): void {
  try {
    localStorage.setItem(LS_KEY, val ? "true" : "false");
  } catch {
    /* SSR or private browsing — ignore */
  }
  setBroadcasterModeRaw(val);
}

export { broadcasterMode };

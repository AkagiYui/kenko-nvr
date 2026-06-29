// Helpers for an arbitrary [from, to) time range expressed in epoch milliseconds
// and edited through <input type="datetime-local"> (which speaks local time).

const DAY_MS = 24 * 60 * 60 * 1000;

// toLocalInput formats an epoch-ms instant as a "YYYY-MM-DDTHH:mm" string in the
// viewer's local timezone, the value format datetime-local expects.
export function toLocalInput(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

// fromLocalInput parses a datetime-local value (local time) back to epoch ms.
export function fromLocalInput(s: string): number {
  const t = new Date(s).getTime();
  return isNaN(t) ? 0 : t;
}

export interface Range {
  from: number;
  to: number;
}

function startOfToday(): number {
  const d = new Date();
  return new Date(d.getFullYear(), d.getMonth(), d.getDate(), 0, 0, 0, 0).getTime();
}

// defaultRange is the initial window: the whole of today (local).
export function defaultRange(): Range {
  const from = startOfToday();
  return { from, to: from + DAY_MS };
}

export interface Preset {
  label: string;
  range: () => Range;
}

// Quick presets offered alongside the manual range inputs.
export const RANGE_PRESETS: Preset[] = [
  { label: "今天", range: () => defaultRange() },
  { label: "近 24 小时", range: () => ({ from: Date.now() - DAY_MS, to: Date.now() }) },
  { label: "近 7 天", range: () => ({ from: Date.now() - 7 * DAY_MS, to: Date.now() }) },
  { label: "近 30 天", range: () => ({ from: Date.now() - 30 * DAY_MS, to: Date.now() }) },
];

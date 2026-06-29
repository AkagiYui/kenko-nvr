// Display formatting helpers, ported verbatim from the original app.js.

// fmtTime renders an epoch-millisecond timestamp as a local date-time string.
// The Date getters below are timezone-local, so each viewer sees the instant in
// their own browser timezone — the server never imposes its zone.
export function fmtTime(t?: number): string {
  if (!t) return "—";
  const d = new Date(t);
  if (isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export function fmtDur(ms?: number): string {
  if (!ms) return "—";
  const s = Math.round(ms / 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
}

// fmtRate renders a bytes-per-second throughput as a human-readable bit rate
// (network traffic is conventionally measured in bits/s).
export function fmtRate(bytesPerSec?: number): string {
  const bits = (bytesPerSec ?? 0) * 8;
  if (bits < 1) return "0 bps";
  const u = ["bps", "Kbps", "Mbps", "Gbps"];
  let i = 0;
  let n = bits;
  while (n >= 1000 && i < u.length - 1) {
    n /= 1000;
    i++;
  }
  return n.toFixed(i ? 1 : 0) + " " + u[i];
}

export function fmtSize(b?: number): string {
  if (!b) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let n = b;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return n.toFixed(i ? 1 : 0) + " " + u[i];
}

// Live status stream: one shared WebSocket pushing every camera's status. The
// caller updates badges and reconciles players as cameras go live or drop.

import { getToken } from "./api";
import type { CameraStatus } from "./types";

export function subscribeStatus(cb: (statuses: Record<string, CameraStatus>) => void): () => void {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(
    `${proto}://${location.host}/api/ws?token=${encodeURIComponent(getToken())}`,
  );
  ws.onmessage = (e) => {
    try {
      cb(JSON.parse(e.data));
    } catch {
      /* ignore malformed frames */
    }
  };
  return () => {
    try {
      ws.close();
    } catch {
      /* ignore */
    }
  };
}

"use strict";

// ---------------------------------------------------------------------------
// State & helpers
// ---------------------------------------------------------------------------
const State = {
  token: localStorage.getItem("nvr_token") || "",
  view: "dashboard",
};

const $ = (sel, root = document) => root.querySelector(sel);
const el = (html) => {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstElementChild;
};
const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])
  );

function toast(msg, kind = "ok") {
  let root = $("#toast-root");
  if (!root) {
    root = el(`<div class="toast" id="toast-root"></div>`);
    document.body.appendChild(root);
  }
  const t = el(`<div class="${kind}">${esc(msg)}</div>`);
  root.appendChild(t);
  setTimeout(() => t.remove(), 4000);
}

async function api(path, opts = {}) {
  const headers = opts.headers || {};
  if (State.token) headers["Authorization"] = "Bearer " + State.token;
  if (opts.body && !(opts.body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  const res = await fetch("/api" + path, { ...opts, headers });
  if (res.status === 401) {
    logout();
    throw new Error("会话已过期，请重新登录");
  }
  if (res.status === 204) return null;
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

function logout() {
  State.token = "";
  localStorage.removeItem("nvr_token");
  stopAllPlayers();
  render();
}

// ---------------------------------------------------------------------------
// Live player — single-connection fMP4-over-WebSocket into MSE (HLS fallback)
//
// A surveillance grid should stream like a real low-latency live site: ONE
// persistent connection per camera, not a playlist poll plus a flood of little
// segment requests. The primary path opens a WebSocket to /api/cameras/{id}/mse,
// receives an fMP4 init segment then one fragment per GOP, and appends them to a
// MediaSource SourceBuffer — continuous, ~1s latency, no sliding-window re-fetch
// loop. When MSE or the codec isn't available (e.g. iOS Safari) we fall back to
// HLS. In every path we muted-autoplay (with a click-to-play overlay if the
// browser blocks autoplay), which is the fix for the original frozen-frame bug.
// ---------------------------------------------------------------------------
const players = new Map(); // video -> { destroy() }

function overlayShow(card, text) {
  const o = $(".video-overlay", card);
  if (!o) return;
  o.onclick = null;
  o.style.display = "flex";
  o.textContent = text;
}
function overlayHide(card) {
  const o = $(".video-overlay", card);
  if (o) { o.style.display = "none"; o.onclick = null; }
}

// tryPlay performs the muted autoplay; if the browser blocks it (no user
// gesture yet) we surface a click-to-play affordance rather than a frozen tile.
function tryPlay(video, card) {
  const p = video.play();
  if (p && p.catch) {
    p.catch(() => {
      const o = $(".video-overlay", card);
      if (!o) return;
      o.style.display = "flex";
      o.textContent = "▶ 点击播放";
      o.onclick = () => video.play().then(() => overlayHide(card)).catch(() => {});
    });
  }
}

function startPlayer(video, cameraId, card) {
  video.muted = true;
  video.playsInline = true;
  if (window.MediaSource) startMsePlayer(video, cameraId, card);
  else startHlsPlayer(video, cameraId, card);
}

// startMsePlayer streams fMP4 over one WebSocket into a MediaSource.
//
// Robustness, in order of importance:
//  * Buffer cushion: we hold playback until ~START_BUFFER seconds are buffered,
//    then play that far behind live, so jitter doesn't underrun the decoder.
//  * Graceful decode recovery: a hardware decode error (MEDIA_ERR_DECODE) kills
//    the media element but NOT the stream. We cache the init segment and, on
//    error, rebuild the MediaSource and resume from the next fragment WITHOUT
//    dropping the WebSocket — a sub-second reseat instead of a full reconnect.
//    (Fragments are keyframe-aligned, so any one is a valid resume point.)
//  * A repeated-failure backstop falls back to a full reconnect with backoff.
function startMsePlayer(video, cameraId, card) {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const url = `${proto}://${location.host}/api/cameras/${cameraId}/mse?token=${encodeURIComponent(State.token)}`;
  const START_BUFFER = 2;   // seconds buffered before playback starts
  const MAX_BUFFER = 30;    // seconds of back-buffer to retain behind playback
  let ms = null, sb = null, ws = null, queue = [], mime = null, initSeg = null;
  let stopped = false, started = false, recovering = false, retry = null, delay = 1000;
  let recoverAt = 0, recoverBurst = 0;

  const closeSocket = () => {
    if (ws) { try { ws.onclose = ws.onmessage = ws.onerror = null; ws.close(); } catch (_) {} ws = null; }
  };
  const fallback = () => { stopped = true; closeSocket(); startHlsPlayer(video, cameraId, card); };
  const fullReconnect = () => {
    closeSocket(); sb = null; queue = []; started = false;
    if (stopped) return;
    overlayShow(card, "重连中…");
    retry = setTimeout(connect, delay);
    delay = Math.min(delay * 2, 8000);
  };

  const pump = () => {
    if (!sb || sb.updating || !queue.length) return;
    try { sb.appendBuffer(queue.shift()); }
    catch (e) { if (e && e.name === "QuotaExceededError") trim(true); /* else: handled by 'error' */ }
  };
  const trim = (hard) => {
    if (!sb || sb.updating || !sb.buffered.length) return;
    const start = sb.buffered.start(0);
    const end = sb.buffered.end(sb.buffered.length - 1);
    // Trim behind playback, but also cap the total span from the live edge so
    // the buffer can never balloon if playback hasn't started (e.g. autoplay
    // was blocked) — otherwise it would grow without bound.
    let cut = video.currentTime - (hard ? 2 : MAX_BUFFER);
    cut = Math.max(cut, end - (MAX_BUFFER + 5));
    if (cut > start + 1) { try { sb.remove(start, cut); } catch (_) {} }
  };
  const onUpdateEnd = () => { trim(false); pump(); maybeStart(); };

  // maybeStart begins playback once the cushion is filled, seeking onto the
  // buffered range first (its timeline can start well above zero after a resume).
  const maybeStart = () => {
    if (started || !sb || !sb.buffered.length) return;
    const begin = sb.buffered.start(0), end = sb.buffered.end(sb.buffered.length - 1);
    if (end - begin < START_BUFFER) return;
    if (video.currentTime < begin || video.currentTime > end) {
      try { video.currentTime = begin; } catch (_) {}
    }
    started = true;
    tryPlay(video, card);
  };

  // recover rebuilds the MediaSource from the cached init after a decode error,
  // keeping the WebSocket. If recoveries come in a tight burst the decoder is
  // genuinely failing, so we drop to a full reconnect instead of spinning.
  const recover = () => {
    if (stopped || recovering || !initSeg || !mime) return;
    const now = Date.now();
    recoverBurst = now - recoverAt < 2500 ? recoverBurst + 1 : 0;
    recoverAt = now;
    if (recoverBurst >= 4) { recoverBurst = 0; fullReconnect(); return; }
    recovering = true; started = false; sb = null; queue = [];
    try { if (ms && ms.readyState === "open") ms.endOfStream(); } catch (_) {}
    ms = new MediaSource();
    video.src = URL.createObjectURL(ms);
    ms.addEventListener("sourceopen", () => {
      URL.revokeObjectURL(video.src);
      try {
        sb = ms.addSourceBuffer(mime);
        sb.mode = "segments";
        sb.addEventListener("updateend", onUpdateEnd);
        queue.push(initSeg);
        pump();
      } catch (_) { recovering = false; fullReconnect(); return; }
      recovering = false;
    }, { once: true });
  };

  const connect = () => {
    if (stopped) return;
    ms = new MediaSource();
    video.src = URL.createObjectURL(ms);
    ms.addEventListener("sourceopen", () => {
      URL.revokeObjectURL(video.src);
      ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      ws.onmessage = (ev) => {
        if (typeof ev.data === "string") {
          try { mime = JSON.parse(ev.data).mimeCodec || ""; } catch (_) {}
          if (!mime || !MediaSource.isTypeSupported(mime)) return fallback();
          try {
            sb = ms.addSourceBuffer(mime);
            sb.mode = "segments";
            sb.addEventListener("updateend", onUpdateEnd);
          } catch (_) { return fallback(); }
          delay = 1000; // connected cleanly
        } else {
          const chunk = new Uint8Array(ev.data);
          if (initSeg === null) initSeg = chunk; // first binary message = init
          queue.push(chunk);
          pump();
        }
      };
      ws.onerror = () => { try { ws.close(); } catch (_) {} };
      ws.onclose = () => fullReconnect();
    }, { once: true });
  };

  video.addEventListener("playing", () => overlayHide(card));
  video.addEventListener("error", () => {
    if (video.error && video.error.code === 3) recover(); // MEDIA_ERR_DECODE
    else fullReconnect();
  });
  // Keep near the live edge: resume if paused with data, and catch up if we
  // drift far behind (e.g. after the tab was backgrounded).
  const keeper = setInterval(() => {
    if (!sb || !sb.buffered.length || video.seeking) return;
    const end = sb.buffered.end(sb.buffered.length - 1);
    if (started && video.readyState >= 2 && video.paused) tryPlay(video, card);
    if (end - video.currentTime > 8) { try { video.currentTime = end - 1; } catch (_) {} }
  }, 3000);

  connect();
  players.set(video, {
    destroy() {
      stopped = true;
      clearInterval(keeper);
      if (retry) clearTimeout(retry);
      closeSocket();
      try { if (ms && ms.readyState === "open") ms.endOfStream(); } catch (_) {}
      video.removeAttribute("src");
      if (video.load) video.load();
    },
  });
}

// startHlsPlayer is the fallback: hls.js (or Safari native HLS) against the HLS
// endpoint. Robust buffering a couple segments behind live; recover on fatal
// errors only.
function startHlsPlayer(video, cameraId, card) {
  const url = `/api/cameras/${cameraId}/hls/index.m3u8`;
  if (window.Hls && Hls.isSupported()) {
    const hls = new Hls({
      lowLatencyMode: false,
      liveSyncDurationCount: 2,
      backBufferLength: 30,
      maxLiveSyncPlaybackRate: 1.5,
      xhrSetup: (xhr) => {
        if (State.token) xhr.setRequestHeader("Authorization", "Bearer " + State.token);
      },
    });
    hls.on(Hls.Events.MANIFEST_PARSED, () => tryPlay(video, card));
    hls.on(Hls.Events.ERROR, (_e, data) => {
      if (!data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) { overlayShow(card, "重连中…"); hls.startLoad(); }
      else if (data.type === Hls.ErrorTypes.MEDIA_ERROR) hls.recoverMediaError();
      else { overlayShow(card, "播放错误：" + (data.details || data.type)); hls.destroy(); }
    });
    video.addEventListener("playing", () => overlayHide(card));
    hls.loadSource(url);
    hls.attachMedia(video);
    players.set(video, { destroy() { try { hls.destroy(); } catch (_) {} } });
  } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
    video.src = url + "?token=" + encodeURIComponent(State.token);
    video.addEventListener("loadedmetadata", () => tryPlay(video, card));
    video.addEventListener("playing", () => overlayHide(card));
    video.addEventListener("error", () => overlayShow(card, "播放错误"));
    players.set(video, { destroy() { video.removeAttribute("src"); if (video.load) video.load(); } });
  } else {
    overlayShow(card, "此浏览器无法在网页中播放该视频流");
  }
}

function stopPlayer(video) {
  const p = players.get(video);
  if (p) { p.destroy(); players.delete(video); }
  video.removeAttribute("src");
  if (video.load) video.load();
}

function stopAllPlayers() {
  for (const v of [...players.keys()]) stopPlayer(v);
}

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------
function modal({ title, body, onOk, okLabel = "保存", width }) {
  const root = $("#modal-root");
  const bg = el(`<div class="modal-bg"><div class="modal" ${width ? `style="width:${width}px"` : ""}>
    <div class="head">${esc(title)}</div>
    <div class="body"></div>
    <div class="foot">
      <button class="ghost" data-act="cancel">取消</button>
      <button class="primary" data-act="ok">${esc(okLabel)}</button>
    </div></div></div>`);
  $(".body", bg).appendChild(body);
  const close = () => bg.remove();
  $('[data-act="cancel"]', bg).onclick = close;
  $('[data-act="ok"]', bg).onclick = async () => {
    try {
      const keep = await onOk();
      if (keep !== false) close();
    } catch (e) {
      toast(e.message, "error");
    }
  };
  bg.onclick = (e) => { if (e.target === bg) close(); };
  root.appendChild(bg);
  return { close };
}

// ---------------------------------------------------------------------------
// Root render
// ---------------------------------------------------------------------------
function render() {
  stopAllPlayers();
  const app = $("#app");
  app.innerHTML = "";
  if (!State.token) {
    app.appendChild(renderLogin());
    return;
  }
  app.appendChild(renderLayout());
  routeView();
}

function renderLayout() {
  const nav = [
    ["dashboard", "实时监看"],
    ["cameras", "摄像头管理"],
    ["recordings", "录像回放"],
    ["settings", "系统设置"],
  ];
  const layout = el(`<div class="layout">
    <div class="sidebar">
      <div class="brand">Kenko NVR<small>pure-go network video recorder</small></div>
      <div class="nav">${nav
        .map(([k, label]) => `<a href="#${k}" data-view="${k}">${label}</a>`)
        .join("")}</div>
      <div class="spacer"></div>
      <div class="logout"><button class="ghost" id="logout-btn">退出登录</button></div>
    </div>
    <div class="content" id="view"></div>
  </div>`);
  $("#logout-btn", layout).onclick = logout;
  return layout;
}

function setActiveNav() {
  document.querySelectorAll(".nav a").forEach((a) =>
    a.classList.toggle("active", a.dataset.view === State.view)
  );
}

function routeView() {
  const hash = (location.hash || "#dashboard").slice(1);
  State.view = ["dashboard", "cameras", "recordings", "settings"].includes(hash)
    ? hash
    : "dashboard";
  setActiveNav();
  const view = $("#view");
  if (!view) return;
  stopAllPlayers();
  view.innerHTML = "";
  ({
    dashboard: viewDashboard,
    cameras: viewCameras,
    recordings: viewRecordings,
    settings: viewSettings,
  }[State.view])(view);
}

window.addEventListener("hashchange", () => {
  if (State.token) routeView();
});

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------
function renderLogin() {
  const wrap = el(`<div class="login-wrap"><div class="login-card">
    <h1>Kenko NVR</h1>
    <p>请登录以继续</p>
    <label>用户名</label><input id="u" autocomplete="username" value="admin" />
    <label>密码</label><input id="p" type="password" autocomplete="current-password" />
    <div style="margin-top:18px"><button class="primary" id="login-btn" style="width:100%">登录</button></div>
  </div></div>`);
  const doLogin = async () => {
    try {
      const data = await api("/login", {
        method: "POST",
        body: { username: $("#u", wrap).value, password: $("#p", wrap).value },
      });
      State.token = data.token;
      localStorage.setItem("nvr_token", data.token);
      location.hash = "#dashboard";
      render();
    } catch (e) {
      toast(e.message, "error");
    }
  };
  $("#login-btn", wrap).onclick = doLogin;
  wrap.addEventListener("keydown", (e) => { if (e.key === "Enter") doLogin(); });
  return wrap;
}

// ---------------------------------------------------------------------------
// Dashboard (live grid)
// ---------------------------------------------------------------------------
async function viewDashboard(view) {
  view.appendChild(el(`<h1>实时监看</h1>`));
  const grid = el(`<div class="grid" id="live-grid"></div>`);
  view.appendChild(grid);

  let cameras = [];
  try {
    cameras = await api("/cameras");
  } catch (e) {
    toast(e.message, "error");
    return;
  }
  if (!cameras.length) {
    grid.replaceWith(el(`<div class="empty-state">还没有摄像头。前往“摄像头管理”添加一个。</div>`));
    return;
  }

  const tiles = new Map(); // cameraId -> { video, card }
  for (const cam of cameras) {
    const card = liveCard(cam);
    tiles.set(cam.id, { video: $("video", card), card });
    grid.appendChild(card);
  }
  // Live status stream: update the badge and reconcile the player so a tile
  // recovers on its own when its source reconnects (or stops when it drops).
  subscribeStatus((statuses) => {
    for (const cam of cameras) {
      const st = statuses[cam.id];
      if (!st) continue;
      const badge = $(`#badge-${cam.id}`);
      if (badge) updateBadge(badge, st);
      reconcilePlayer(tiles.get(cam.id), cam, st);
    }
  });
}

// reconcilePlayer starts or stops a tile's player as the camera goes live or
// drops, so the grid is self-healing across reconnects.
function reconcilePlayer(tile, cam, st) {
  if (!tile) return;
  const playing = players.has(tile.video);
  if (st.live && !playing) {
    overlayShow(tile.card, "加载中…");
    startPlayer(tile.video, cam.id, tile.card);
  } else if (!st.live && playing) {
    stopPlayer(tile.video);
    overlayShow(tile.card, st.error ? "无信号：" + st.error : "等待视频流…");
  }
}

function updateBadge(badge, st) {
  badge.className = "badge " + (st.state || "");
  let txt = { running: "在线", connecting: "连接中", error: "错误", idle: "空闲" }[st.state] || st.state;
  if (st.recording) txt += " · 录像中";
  badge.textContent = txt;
  badge.title = st.error || "";
}

function liveCard(cam) {
  const st = cam.status || {};
  const card = el(`<div class="card">
    <div class="head">
      <span class="title">${esc(cam.name)}</span>
      <span class="badge ${esc(st.state || "")}" id="badge-${cam.id}">…</span>
    </div>
    <div class="video-wrap">
      <video muted playsinline autoplay></video>
      <div class="video-overlay" style="display:none"></div>
    </div>
    <div class="body" data-ptz></div>
  </div>`);
  const video = $("video", card);
  updateBadge($(`#badge-${cam.id}`, card), st);

  if (st.live) {
    overlayShow(card, "加载中…");
    startPlayer(video, cam.id, card);
  } else {
    overlayShow(card, st.error ? "无信号：" + st.error : "等待视频流…");
  }

  if (cam.onvifEnabled) card.querySelector("[data-ptz]").appendChild(ptzControls(cam));
  else card.querySelector("[data-ptz]").remove();
  return card;
}

function ptzControls(cam) {
  const wrap = el(`<div>
    <div class="ptz">
      <button class="empty"></button><button data-dir="up">▲</button><button class="empty"></button>
      <button data-dir="left">◀</button><button data-dir="stop">■</button><button data-dir="right">▶</button>
      <button class="empty"></button><button data-dir="down">▼</button><button class="empty"></button>
    </div>
    <div class="row" style="margin-top:8px">
      <button data-zoom="in">放大 +</button>
      <button data-zoom="out">缩小 −</button>
    </div>
  </div>`);
  const move = (pan, tilt, zoom) =>
    api(`/cameras/${cam.id}/ptz`, { method: "POST", body: { action: "move", pan, tilt, zoom } })
      .catch((e) => toast(e.message, "error"));
  const stop = () =>
    api(`/cameras/${cam.id}/ptz`, { method: "POST", body: { action: "stop" } })
      .catch((e) => toast(e.message, "error"));

  const dirMap = { up: [0, 0.6, 0], down: [0, -0.6, 0], left: [-0.6, 0, 0], right: [0.6, 0, 0] };
  wrap.querySelectorAll("[data-dir]").forEach((b) => {
    const dir = b.dataset.dir;
    if (dir === "stop") { b.onclick = stop; return; }
    const [p, t, z] = dirMap[dir];
    b.addEventListener("mousedown", () => move(p, t, z));
    b.addEventListener("mouseup", stop);
    b.addEventListener("mouseleave", stop);
  });
  wrap.querySelectorAll("[data-zoom]").forEach((b) => {
    const z = b.dataset.zoom === "in" ? 0.6 : -0.6;
    b.addEventListener("mousedown", () => move(0, 0, z));
    b.addEventListener("mouseup", stop);
    b.addEventListener("mouseleave", stop);
  });
  return wrap;
}

// status websocket (single shared connection per view)
let statusWS = null;
function subscribeStatus(cb) {
  if (statusWS) { try { statusWS.close(); } catch (_) {} }
  const proto = location.protocol === "https:" ? "wss" : "ws";
  statusWS = new WebSocket(`${proto}://${location.host}/api/ws?token=${encodeURIComponent(State.token)}`);
  statusWS.onmessage = (e) => { try { cb(JSON.parse(e.data)); } catch (_) {} };
}

// ---------------------------------------------------------------------------
// Cameras management
// ---------------------------------------------------------------------------
async function viewCameras(view) {
  const head = el(`<div class="toolbar">
    <h1 style="margin:0">摄像头管理</h1><div class="spacer"></div>
    <button class="ghost" id="discover-btn">ONVIF 发现</button>
    <button class="primary" id="add-btn">+ 添加摄像头</button>
  </div>`);
  view.appendChild(head);
  const tableWrap = el(`<div class="card"><table>
    <thead><tr><th>名称</th><th>类型</th><th>地址</th><th>状态</th><th>录像</th><th>ONVIF</th><th></th></tr></thead>
    <tbody></tbody></table></div>`);
  view.appendChild(tableWrap);

  $("#add-btn", head).onclick = () => cameraForm(null, () => viewCameras(clear(view)));
  $("#discover-btn", head).onclick = onvifDiscover;

  let cameras = [];
  try { cameras = await api("/cameras"); } catch (e) { return toast(e.message, "error"); }

  const tbody = $("tbody", tableWrap);
  if (!cameras.length) {
    tbody.appendChild(el(`<tr><td colspan="7" class="muted" style="text-align:center;padding:30px">暂无摄像头</td></tr>`));
    return;
  }
  for (const cam of cameras) {
    const st = cam.status || {};
    const tr = el(`<tr>
      <td>${esc(cam.name)}</td>
      <td>${esc(cam.sourceType.toUpperCase())}</td>
      <td class="small muted">${esc(cam.sourceType === "rtmp" ? "推流: /live/" + cam.id : cam.url)}</td>
      <td><span class="badge ${esc(st.state || "")}">${esc(st.state || "idle")}</span></td>
      <td>${cam.record ? "✓" : "—"}</td>
      <td>${cam.onvifEnabled ? "✓" : "—"}</td>
      <td style="text-align:right;white-space:nowrap">
        <button class="ghost small" data-edit>编辑</button>
        <button class="danger small" data-del>删除</button>
      </td>
    </tr>`);
    $("[data-edit]", tr).onclick = () => cameraForm(cam, () => viewCameras(clear(view)));
    $("[data-del]", tr).onclick = async () => {
      if (!confirm(`删除摄像头 “${cam.name}”？其录像记录也会被移除。`)) return;
      try { await api(`/cameras/${cam.id}`, { method: "DELETE" }); toast("已删除"); viewCameras(clear(view)); }
      catch (e) { toast(e.message, "error"); }
    };
    tbody.appendChild(tr);
  }
}

function clear(view) { view.innerHTML = ""; return view; }

function cameraForm(cam, onSaved, seed) {
  const c = cam || Object.assign(
    { sourceType: "rtsp", enabled: true, record: false, transport: "", onvifEnabled: false },
    seed || {}
  );
  const body = el(`<div>
    <label>名称</label><input data-f="name" value="${esc(c.name || "")}" />
    <div class="row">
      <div><label>来源类型</label>
        <select data-f="sourceType">
          <option value="rtsp" ${c.sourceType === "rtsp" ? "selected" : ""}>RTSP 拉流</option>
          <option value="onvif" ${c.sourceType === "onvif" ? "selected" : ""}>ONVIF（自动获取视频流 + 云台）</option>
          <option value="rtmp" ${c.sourceType === "rtmp" ? "selected" : ""}>RTMP 推流（设备推到本机）</option>
        </select>
      </div>
      <div><label>RTSP 传输</label>
        <select data-f="transport">
          <option value="" ${!c.transport ? "selected" : ""}>自动</option>
          <option value="tcp" ${c.transport === "tcp" ? "selected" : ""}>TCP</option>
          <option value="udp" ${c.transport === "udp" ? "selected" : ""}>UDP</option>
        </select>
      </div>
    </div>
    <div data-rtsp>
      <label>RTSP 地址 <span class="muted small">rtsp://host:554/stream</span></label>
      <input data-f="url" value="${esc(c.url || "")}" placeholder="rtsp://..." />
      <div class="row" style="margin-top:4px">
        <div><label>用户名</label><input data-f="username" value="${esc(c.username || "")}" /></div>
        <div><label>密码</label><input data-f="password" type="password" placeholder="${cam ? "（留空表示不修改）" : ""}" /></div>
      </div>
    </div>
    <div data-rtmp style="display:none">
      <p class="muted small">设备/编码器请推流到：<code>rtmp://&lt;本机IP&gt;:1935/live/${esc(c.id || "<保存后生成的ID>")}</code></p>
    </div>
    <div data-onvif-note style="display:none">
      <p class="muted small">视频流地址将通过 ONVIF 自动获取（设备地址与账号在下方填写）。</p>
    </div>
    <div class="checkbox" style="margin-top:12px"><input type="checkbox" data-f="record" ${c.record ? "checked" : ""} id="rec-cb" /><label for="rec-cb" style="margin:0">启用录像</label></div>
    <div class="checkbox" style="margin-top:8px"><input type="checkbox" data-f="enabled" ${c.enabled ? "checked" : ""} id="en-cb" /><label for="en-cb" style="margin:0">启用此摄像头</label></div>

    <hr style="border-color:var(--border);margin:16px 0" />
    <div class="checkbox" data-onvif-toggle><input type="checkbox" data-f="onvifEnabled" ${c.onvifEnabled ? "checked" : ""} id="onvif-cb" /><label for="onvif-cb" style="margin:0">启用 ONVIF 控制（云台 PTZ）</label></div>
    <div data-onvif style="display:${c.onvifEnabled ? "block" : "none"}">
      <label>ONVIF 设备地址 <span class="muted small">填 host 或 host:port（粘贴发现的完整 URL 也可）</span></label>
      <input data-f="onvifXAddr" value="${esc(c.onvifXAddr || "")}" placeholder="192.168.5.19" />
      <div class="row">
        <div><label>ONVIF 用户名</label><input data-f="onvifUsername" value="${esc(c.onvifUsername || "")}" /></div>
        <div><label>ONVIF 密码</label><input data-f="onvifPassword" type="password" placeholder="${cam ? "（留空表示不修改）" : ""}" /></div>
      </div>
      <label>Profile Token <span class="muted small">留空则自动使用第一个</span></label>
      <input data-f="onvifProfile" value="${esc(c.onvifProfile || "")}" />
      <button class="ghost small" data-probe style="margin-top:8px">探测 ONVIF（获取 RTSP 地址）</button>
    </div>
  </div>`);

  const onvifCb = $('[data-f="onvifEnabled"]', body);
  const toggle = () => {
    const type = $('[data-f="sourceType"]', body).value;
    $("[data-rtsp]", body).style.display = type === "rtsp" ? "block" : "none";
    $("[data-rtmp]", body).style.display = type === "rtmp" ? "block" : "none";
    $("[data-onvif-note]", body).style.display = type === "onvif" ? "block" : "none";
    // For an ONVIF source, control is implied: force-enable and hide the toggle.
    const toggleRow = $("[data-onvif-toggle]", body);
    if (type === "onvif") {
      onvifCb.checked = true;
      toggleRow.style.display = "none";
      $("[data-onvif]", body).style.display = "block";
    } else {
      toggleRow.style.display = "flex";
      $("[data-onvif]", body).style.display = onvifCb.checked ? "block" : "none";
    }
  };
  $('[data-f="sourceType"]', body).onchange = toggle;
  toggle();
  onvifCb.onchange = toggle;
  $("[data-probe]", body).onclick = () => onvifProbe(body);

  const val = (f) => {
    const node = $(`[data-f="${f}"]`, body);
    return node.type === "checkbox" ? node.checked : node.value;
  };

  modal({
    title: cam ? "编辑摄像头" : "添加摄像头",
    body,
    onOk: async () => {
      const payload = {
        name: val("name"), sourceType: val("sourceType"), url: val("url") || "",
        username: val("username") || "", password: val("password") || "",
        transport: val("transport") || "", record: val("record"), enabled: val("enabled"),
        onvifEnabled: val("onvifEnabled"), onvifXAddr: val("onvifXAddr") || "",
        onvifUsername: val("onvifUsername") || "", onvifPassword: val("onvifPassword") || "",
        onvifProfile: val("onvifProfile") || "",
      };
      if (cam) await api(`/cameras/${cam.id}`, { method: "PUT", body: payload });
      else await api("/cameras", { method: "POST", body: payload });
      toast("已保存");
      onSaved();
    },
  });
}

async function onvifProbe(formBody) {
  const xaddr = $('[data-f="onvifXAddr"]', formBody).value;
  const username = $('[data-f="onvifUsername"]', formBody).value;
  const password = $('[data-f="onvifPassword"]', formBody).value;
  if (!xaddr) return toast("请先填写 ONVIF 设备地址", "error");
  toast("正在探测…");
  try {
    const res = await api("/onvif/probe", { method: "POST", body: { xaddr, username, password } });
    if (!res.profiles || !res.profiles.length) return toast("未发现 profile", "error");
    const p = res.profiles[0];
    if (p.streamUri) {
      $('[data-f="url"]', formBody).value = p.streamUri;
      $('[data-f="onvifProfile"]', formBody).value = p.token;
      toast(`已填入 RTSP 地址（${res.info.manufacturer} ${res.info.model}）`);
    } else {
      toast("已连接，但未获取到 RTSP 地址", "error");
    }
  } catch (e) { toast(e.message, "error"); }
}

async function onvifDiscover() {
  const body = el(`<div><p class="muted">正在局域网内搜索 ONVIF 设备…</p><div data-list></div></div>`);
  const m = modal({ title: "ONVIF 设备发现", body, okLabel: "关闭", onOk: () => {} });
  try {
    const devices = await api("/onvif/discover");
    const list = $("[data-list]", body);
    list.innerHTML = "";
    if (!devices || !devices.length) {
      list.appendChild(el(`<p>未发现设备。请确认设备与本机在同一网段。</p>`));
      return;
    }
    for (const d of devices) {
      let host = d.xaddr;
      try { host = new URL(d.xaddr).host; } catch (_) {}
      const row = el(`<div class="card" style="margin-bottom:8px"><div class="body small" style="display:flex;align-items:center;gap:10px">
        <div style="flex:1;min-width:0">
          <div><b>${esc(host)}</b></div>
          <div class="muted" style="word-break:break-all">${esc(d.xaddr)}</div>
        </div>
        <button class="primary small" data-add>添加为摄像头</button>
      </div></div>`);
      $("[data-add]", row).onclick = () => {
        m.close();
        cameraForm(null, routeView, { sourceType: "onvif", onvifXAddr: host, name: host });
      };
      list.appendChild(row);
    }
  } catch (e) { toast(e.message, "error"); }
}

// ---------------------------------------------------------------------------
// Recordings
// ---------------------------------------------------------------------------
async function viewRecordings(view) {
  view.appendChild(el(`<h1>录像回放</h1>`));
  const bar = el(`<div class="toolbar">
    <select id="rec-cam" style="max-width:240px"><option value="">全部摄像头</option></select>
    <button class="ghost" id="rec-refresh">刷新</button>
  </div>`);
  view.appendChild(bar);
  const tableWrap = el(`<div class="card"><table>
    <thead><tr><th>摄像头</th><th>开始时间</th><th>时长</th><th>大小</th><th>S3</th><th></th></tr></thead>
    <tbody></tbody></table></div>`);
  view.appendChild(tableWrap);

  let cameras = [];
  try { cameras = await api("/cameras"); } catch (_) {}
  const camById = {};
  for (const c of cameras) {
    camById[c.id] = c.name;
    $("#rec-cam", bar).appendChild(el(`<option value="${c.id}">${esc(c.name)}</option>`));
  }

  const load = async () => {
    const camId = $("#rec-cam", bar).value;
    const tbody = $("tbody", tableWrap);
    tbody.innerHTML = "";
    let recs = [];
    try { recs = await api("/recordings?limit=500" + (camId ? "&cameraId=" + camId : "")); }
    catch (e) { return toast(e.message, "error"); }
    if (!recs.length) { tbody.appendChild(el(`<tr><td colspan="6" class="muted" style="text-align:center;padding:30px">暂无录像</td></tr>`)); return; }
    for (const r of recs) {
      const tr = el(`<tr>
        <td>${esc(camById[r.cameraId] || r.cameraId)}</td>
        <td>${esc(fmtTime(r.startTime))}</td>
        <td>${esc(fmtDur(r.durationMs))}</td>
        <td>${esc(fmtSize(r.sizeBytes))}</td>
        <td>${r.uploaded ? '<span class="badge running">已上传</span>' : "—"}</td>
        <td style="text-align:right;white-space:nowrap">
          <button class="ghost small" data-play ${r.complete ? "" : "disabled"}>播放</button>
          <a class="small" href="/api/recordings/${r.id}/file?download=1&token=${encodeURIComponent(State.token)}">下载</a>
          <button class="danger small" data-del>删除</button>
        </td>
      </tr>`);
      $("[data-play]", tr).onclick = () => playRecording(r, camById[r.cameraId]);
      $("[data-del]", tr).onclick = async () => {
        if (!confirm("删除此录像文件？")) return;
        try { await api(`/recordings/${r.id}`, { method: "DELETE" }); toast("已删除"); load(); }
        catch (e) { toast(e.message, "error"); }
      };
      tbody.appendChild(tr);
    }
  };
  $("#rec-refresh", bar).onclick = load;
  $("#rec-cam", bar).onchange = load;
  load();
}

function playRecording(r, camName) {
  const body = el(`<div><video controls autoplay style="width:100%;background:#000;border-radius:8px"
    src="/api/recordings/${r.id}/file?token=${encodeURIComponent(State.token)}"></video>
    <p class="muted small">${esc(camName || "")} · ${esc(fmtTime(r.startTime))}</p></div>`);
  modal({ title: "录像回放", body, okLabel: "关闭", width: 760, onOk: () => {} });
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------
async function viewSettings(view) {
  view.appendChild(el(`<h1>系统设置</h1>`));
  const grid = el(`<div class="grid" style="grid-template-columns:1fr"></div>`);
  view.appendChild(grid);
  grid.appendChild(await recordingSettingsCard());
  grid.appendChild(await retentionSettingsCard());
  grid.appendChild(await s3SettingsCard());
}

async function recordingSettingsCard() {
  const c = await api("/settings/recording");
  const card = el(`<div class="card"><div class="head"><span class="title">录像设置</span></div>
    <div class="body">
      <div class="row">
        <div><label>单文件时长（秒）</label><input data-f="segmentSeconds" type="number" value="${c.segmentSeconds}" /></div>
      </div>
      <label>文件命名规则</label>
      <input data-f="pathTemplate" value="${esc(c.pathTemplate)}" />
      <p class="muted small">可用占位符：{camera} {camera_id} {year} {month} {day} {hour} {minute} {second} {unix}</p>
      <button class="primary" data-save style="margin-top:8px">保存</button>
    </div></div>`);
  $("[data-save]", card).onclick = async () => {
    try {
      await api("/settings/recording", { method: "PUT", body: {
        segmentSeconds: parseInt($('[data-f="segmentSeconds"]', card).value, 10) || 600,
        pathTemplate: $('[data-f="pathTemplate"]', card).value,
      }});
      toast("已保存（下个录像分段生效）");
    } catch (e) { toast(e.message, "error"); }
  };
  return card;
}

async function retentionSettingsCard() {
  const p = await api("/settings/retention");
  const card = el(`<div class="card"><div class="head"><span class="title">存储保留策略（滚动删除）</span></div>
    <div class="body">
      <div class="checkbox"><input type="checkbox" data-f="enabled" ${p.enabled ? "checked" : ""} id="ret-en" /><label for="ret-en" style="margin:0">启用自动清理</label></div>
      <div class="row" style="margin-top:8px">
        <div><label>最长保留天数（0=不限）</label><input data-f="maxAgeDays" type="number" value="${p.maxAgeDays}" /></div>
        <div><label>最大总容量 GB（0=不限）</label><input data-f="maxTotalSizeGB" type="number" step="0.1" value="${p.maxTotalSizeGB}" /></div>
      </div>
      <div class="row">
        <div><label>最小剩余磁盘空间 GB（0=不限）</label><input data-f="minFreeSpaceGB" type="number" step="0.1" value="${p.minFreeSpaceGB}" /></div>
      </div>
      <div class="checkbox" style="margin-top:8px"><input type="checkbox" data-f="deleteAfterUpload" ${p.deleteAfterUpload ? "checked" : ""} id="ret-up" /><label for="ret-up" style="margin:0">仅删除已上传到 S3 的录像</label></div>
      <button class="primary" data-save style="margin-top:12px">保存</button>
    </div></div>`);
  $("[data-save]", card).onclick = async () => {
    try {
      await api("/settings/retention", { method: "PUT", body: {
        enabled: $('[data-f="enabled"]', card).checked,
        maxAgeDays: parseInt($('[data-f="maxAgeDays"]', card).value, 10) || 0,
        maxTotalSizeGB: parseFloat($('[data-f="maxTotalSizeGB"]', card).value) || 0,
        minFreeSpaceGB: parseFloat($('[data-f="minFreeSpaceGB"]', card).value) || 0,
        deleteAfterUpload: $('[data-f="deleteAfterUpload"]', card).checked,
      }});
      toast("已保存");
    } catch (e) { toast(e.message, "error"); }
  };
  return card;
}

async function s3SettingsCard() {
  const c = await api("/settings/s3");
  const card = el(`<div class="card"><div class="head"><span class="title">S3 录像上传</span></div>
    <div class="body">
      <div class="checkbox"><input type="checkbox" data-f="enabled" ${c.enabled ? "checked" : ""} id="s3-en" /><label for="s3-en" style="margin:0">启用上传</label></div>
      <div class="row">
        <div><label>Endpoint</label><input data-f="endpoint" value="${esc(c.endpoint || "")}" placeholder="s3.amazonaws.com" /></div>
        <div><label>Region</label><input data-f="region" value="${esc(c.region || "")}" placeholder="us-east-1" /></div>
      </div>
      <div class="row">
        <div><label>Bucket</label><input data-f="bucket" value="${esc(c.bucket || "")}" /></div>
        <div><label>Key 前缀</label><input data-f="keyPrefix" value="${esc(c.keyPrefix || "")}" placeholder="nvr/" /></div>
      </div>
      <div class="row">
        <div><label>Access Key</label><input data-f="accessKey" value="${esc(c.accessKey || "")}" /></div>
        <div><label>Secret Key</label><input data-f="secretKey" type="password" placeholder="（留空表示不修改）" /></div>
      </div>
      <label>HTTP 代理 <span class="muted small">可选，例如 http://user:pass@proxy:3128</span></label>
      <input data-f="proxyURL" value="${esc(c.proxyURL || "")}" placeholder="http://proxy:3128" />
      <div class="row" style="margin-top:8px">
        <div class="checkbox"><input type="checkbox" data-f="useSSL" ${c.useSSL ? "checked" : ""} id="s3-ssl" /><label for="s3-ssl" style="margin:0">使用 HTTPS</label></div>
        <div class="checkbox"><input type="checkbox" data-f="deleteLocalAfterUpload" ${c.deleteLocalAfterUpload ? "checked" : ""} id="s3-del" /><label for="s3-del" style="margin:0">上传后删除本地文件</label></div>
      </div>
      <div style="margin-top:12px;display:flex;gap:10px">
        <button class="primary" data-save>保存</button>
        <button class="ghost" data-test>测试连接</button>
      </div>
    </div></div>`);
  const collect = () => ({
    enabled: $('[data-f="enabled"]', card).checked,
    endpoint: $('[data-f="endpoint"]', card).value,
    region: $('[data-f="region"]', card).value,
    bucket: $('[data-f="bucket"]', card).value,
    keyPrefix: $('[data-f="keyPrefix"]', card).value,
    accessKey: $('[data-f="accessKey"]', card).value,
    secretKey: $('[data-f="secretKey"]', card).value,
    proxyURL: $('[data-f="proxyURL"]', card).value,
    useSSL: $('[data-f="useSSL"]', card).checked,
    deleteLocalAfterUpload: $('[data-f="deleteLocalAfterUpload"]', card).checked,
  });
  $("[data-save]", card).onclick = async () => {
    try { await api("/settings/s3", { method: "PUT", body: collect() }); toast("已保存"); }
    catch (e) { toast(e.message, "error"); }
  };
  $("[data-test]", card).onclick = async () => {
    toast("正在测试…");
    try { await api("/settings/s3/test", { method: "POST", body: collect() }); toast("连接成功 ✓"); }
    catch (e) { toast("失败：" + e.message, "error"); }
  };
  return card;
}

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------
function fmtTime(t) {
  if (!t) return "—";
  const d = new Date(t);
  if (isNaN(d)) return "—";
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
function fmtDur(ms) {
  if (!ms) return "—";
  const s = Math.round(ms / 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
}
function fmtSize(b) {
  if (!b) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, n = b;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(i ? 1 : 0) + " " + u[i];
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------
render();

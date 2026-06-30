import { createFileRoute } from "@tanstack/solid-router";
import { createEffect, createMemo, createResource, createSignal, For, onCleanup, onMount, Show } from "solid-js";
import { Icon } from "@iconify-icon/solid";
import { api, atLeastOperator, getToken, isAdmin } from "~/lib/api";
import { toast } from "~/components/toast";
import { Modal } from "~/components/Modal";
import { RecordingPlayer } from "~/components/RecordingPlayer";
import { fmtTime } from "~/lib/format";
import type { FaceConfig, FaceStatus, FaceTrack, Person, PersonDetail, Recording } from "~/lib/types";

export const Route = createFileRoute("/_authed/people")({
  component: People,
});

const personThumb = (id: string) => `/api/persons/${id}/thumb?token=${encodeURIComponent(getToken())}`;
const faceThumb = (id: string) => `/api/faces/${id}/thumb?token=${encodeURIComponent(getToken())}`;

interface Playing {
  rec: Recording;
  offset: number;
}

function People() {
  const op = atLeastOperator();
  const [persons, { refetch }] = createResource<Person[]>(() => api<Person[]>("/persons"));
  const [selected, setSelected] = createSignal<string | null>(null);
  const [playing, setPlaying] = createSignal<Playing | null>(null);
  const [settingsOpen, setSettingsOpen] = createSignal(false);
  const [mergeMode, setMergeMode] = createSignal(false);
  const [picked, setPicked] = createSignal<string[]>([]);

  // Feature/sidecar/queue status, polled.
  const [status, { refetch: refetchStatus }] = createResource<FaceStatus>(() => api<FaceStatus>("/face/status"));
  onMount(() => {
    const t = setInterval(() => void refetchStatus(), 5000);
    onCleanup(() => clearInterval(t));
  });

  const togglePick = (id: string) =>
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));

  const doMerge = async () => {
    const ids = picked();
    if (ids.length < 2) {
      toast("请选择至少两个人物进行合并", "error");
      return;
    }
    const [target, ...sources] = ids;
    try {
      await api("/persons/merge", { method: "POST", body: { targetId: target, sourceIds: sources } });
      toast("已合并");
      setPicked([]);
      setMergeMode(false);
      void refetch();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const scan = async () => {
    try {
      const r = await api<{ enqueued: number; matched: number }>("/face/scan", { method: "POST", body: {} });
      toast(`已加入分析队列：${r.enqueued} 个录像`);
      void refetchStatus();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const recluster = async () => {
    try {
      const r = await api<{ moved: number; personsDeleted: number; personsAfter: number }>("/face/recluster", {
        method: "POST",
      });
      toast(`重新聚类完成：移动 ${r.moved}，合并删除 ${r.personsDeleted}，现有 ${r.personsAfter} 人`);
      void refetch();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <>
      <div class="flex items-center justify-between mb-5 flex-wrap gap-3">
        <h1 class="text-[22px] font-semibold">人物</h1>
        <div class="flex items-center gap-2 flex-wrap">
          <StatusChip status={status()} />
          <Show when={op}>
            <button class="btn btn-sm" onClick={() => void scan()} title="为已有录像排队人脸分析">
              <Icon icon="lucide:scan-face" width="16" height="16" /> 扫描录像
            </button>
            <button class="btn btn-sm" onClick={() => void recluster()} title="重新聚类，清理重复人物">
              <Icon icon="lucide:git-merge" width="16" height="16" /> 重新聚类
            </button>
            <button
              class="btn btn-sm"
              classList={{ "btn-primary": mergeMode() }}
              onClick={() => {
                setMergeMode((v) => !v);
                setPicked([]);
              }}
            >
              {mergeMode() ? `合并所选 (${picked().length})` : "合并人物"}
            </button>
            <Show when={mergeMode() && picked().length >= 2}>
              <button class="btn btn-sm btn-primary" onClick={() => void doMerge()}>
                确认合并
              </button>
            </Show>
          </Show>
          <Show when={isAdmin()}>
            <button class="btn btn-sm btn-ghost" onClick={() => setSettingsOpen(true)} title="人脸识别设置">
              <Icon icon="lucide:settings" width="16" height="16" />
            </button>
          </Show>
        </div>
      </div>

      <Show
        when={persons() && persons()!.length}
        fallback={
          <div class="text-center text-base-content/60 py-16">
            {persons.loading ? "加载中…" : "暂无识别到的人物。开启人脸识别并点击「扫描录像」后将自动出现。"}
          </div>
        }
      >
        <div class="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-4">
          <For each={persons()}>
            {(p) => (
              <div
                class="card bg-base-200 border border-base-300 overflow-hidden cursor-pointer hover:border-primary transition-colors"
                classList={{ "ring-2 ring-primary": mergeMode() && picked().includes(p.id) }}
                onClick={() => (mergeMode() ? togglePick(p.id) : setSelected(p.id))}
              >
                <div class="aspect-square bg-base-300 overflow-hidden">
                  <img
                    src={personThumb(p.id)}
                    class="w-full h-full object-cover"
                    loading="lazy"
                    onError={(e) => (e.currentTarget.style.visibility = "hidden")}
                  />
                </div>
                <div class="p-2.5">
                  <div class="font-medium truncate text-sm">
                    {p.name || <span class="text-base-content/50">未命名</span>}
                  </div>
                  <div class="text-[11px] text-base-content/50 mt-0.5">
                    {p.faceCount} 张 · {p.lastSeen ? fmtTime(p.lastSeen) : "—"}
                  </div>
                </div>
              </div>
            )}
          </For>
        </div>
      </Show>

      <Show when={selected()}>
        {(id) => (
          <PersonModal
            id={id()}
            canEdit={op}
            onClose={() => setSelected(null)}
            onChanged={() => void refetch()}
            onPlay={(rec, offset) => setPlaying({ rec, offset })}
          />
        )}
      </Show>

      <Show when={playing()}>
        {(p) => (
          <Modal title="录像回放" hideOk width={760} onClose={() => setPlaying(null)}>
            <RecordingPlayer recordingId={p().rec.id} offsetSec={p().offset} />
            <p class="text-sm text-base-content/60 mt-2">{fmtTime(p().rec.startTime)}</p>
          </Modal>
        )}
      </Show>

      <Show when={settingsOpen()}>
        <FaceSettingsModal onClose={() => setSettingsOpen(false)} onSaved={() => void refetchStatus()} />
      </Show>
    </>
  );
}

function StatusChip(props: { status?: FaceStatus }) {
  const s = () => props.status;
  const running = () => (s()?.jobs?.["running"] ?? 0) + (s()?.jobs?.["pending"] ?? 0);
  return (
    <Show when={s()}>
      <div class="flex items-center gap-2 text-xs">
        <span
          class="badge"
          classList={{
            "badge-success": !!s()!.enabled && !!s()!.sidecarOk,
            "badge-warning": !!s()!.enabled && !s()!.sidecarOk,
            "badge-ghost": !s()!.enabled,
          }}
          title={s()!.sidecarErr || s()!.sidecar?.model || ""}
        >
          {!s()!.enabled ? "已禁用" : s()!.sidecarOk ? `就绪 · ${s()!.sidecar?.model ?? ""}` : "推理服务离线"}
        </span>
        <Show when={running() > 0}>
          <span class="text-base-content/50">队列 {running()}</span>
        </Show>
      </div>
    </Show>
  );
}

function PersonModal(props: {
  id: string;
  canEdit: boolean;
  onClose: () => void;
  onChanged: () => void;
  onPlay: (rec: Recording, offset: number) => void;
}) {
  const [detail, { refetch }] = createResource<PersonDetail, string>(
    () => props.id,
    (id) => api<PersonDetail>(`/persons/${id}?withRecordings=1`),
  );
  const [name, setName] = createSignal("");
  createEffect(() => {
    const d = detail();
    if (d) setName(d.name || "");
  });

  const recById = createMemo(() => {
    const m: Record<string, Recording> = {};
    for (const r of detail()?.recordings ?? []) m[r.id] = r;
    return m;
  });

  const save = async () => {
    try {
      await api(`/persons/${props.id}`, { method: "PATCH", body: { name: name() } });
      toast("已保存");
      void refetch();
      props.onChanged();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const del = async () => {
    if (!confirm("删除此人物？其人脸将变为未分配，可在下次聚类时重新归类。")) return;
    try {
      await api(`/persons/${props.id}`, { method: "DELETE" });
      toast("已删除");
      props.onChanged();
      props.onClose();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const split = async (track: FaceTrack) => {
    try {
      await api(`/persons/${props.id}/split`, { method: "POST", body: { trackIds: [track.id] } });
      toast("已拆分为新人物");
      void refetch();
      props.onChanged();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const play = (track: FaceTrack) => {
    const rec = recById()[track.recordingId];
    if (!rec) {
      toast("找不到对应录像", "error");
      return;
    }
    props.onPlay(rec, (track.bestOffsetMs ?? 0) / 1000);
  };

  return (
    <Modal title="人物详情" hideOk width={820} onClose={props.onClose}>
      <Show when={detail()} fallback={<div class="py-8 text-center text-base-content/60">加载中…</div>}>
        <div class="flex items-start gap-4 mb-4">
          <img src={personThumb(props.id)} class="w-24 h-24 rounded-lg object-cover bg-base-300" />
          <div class="flex-1">
            <Show
              when={props.canEdit}
              fallback={<div class="text-lg font-semibold">{detail()!.name || "未命名"}</div>}
            >
              <div class="flex items-center gap-2">
                <input
                  class="input input-bordered input-sm flex-1 max-w-[280px]"
                  placeholder="未命名"
                  value={name()}
                  onInput={(e) => setName(e.currentTarget.value)}
                />
                <button class="btn btn-sm btn-primary" onClick={() => void save()}>
                  保存
                </button>
              </div>
            </Show>
            <div class="text-xs text-base-content/50 mt-2">
              {detail()!.faceCount} 张人脸 · {detail()!.appearances.length} 次出现
              <Show when={detail()!.firstSeen}>
                {" "}
                · 首次 {fmtTime(detail()!.firstSeen)} · 最近 {fmtTime(detail()!.lastSeen)}
              </Show>
            </div>
            <Show when={props.canEdit}>
              <button class="btn btn-xs btn-error btn-outline mt-2" onClick={() => void del()}>
                删除人物
              </button>
            </Show>
          </div>
        </div>

        <div class="text-sm font-medium mb-2">出现记录</div>
        <div class="grid grid-cols-[repeat(auto-fill,minmax(120px,1fr))] gap-3 max-h-[46vh] overflow-y-auto pr-1">
          <For each={detail()!.appearances}>
            {(t) => (
              <div class="card bg-base-200 border border-base-300 overflow-hidden">
                <button class="aspect-square bg-base-300 relative group" onClick={() => play(t)} title="跳转播放">
                  <Show when={t.bestFaceId}>
                    <img src={faceThumb(t.bestFaceId!)} class="w-full h-full object-cover" loading="lazy" />
                  </Show>
                  <span class="absolute inset-0 flex items-center justify-center bg-black/30 opacity-0 group-hover:opacity-100">
                    <Icon icon="lucide:play" width="22" height="22" class="text-white" />
                  </span>
                </button>
                <div class="p-2 text-[11px] text-base-content/60">
                  <div>{t.startTs ? fmtTime(t.startTs) : "—"}</div>
                  <div class="flex items-center justify-between mt-1">
                    <span>{t.faceCount} 张{t.confirmed ? " · 已确认" : ""}</span>
                    <Show when={props.canEdit}>
                      <button class="link link-hover text-base-content/50" onClick={() => void split(t)}>
                        拆分
                      </button>
                    </Show>
                  </div>
                </div>
              </div>
            )}
          </For>
        </div>
      </Show>
    </Modal>
  );
}

function FaceSettingsModal(props: { onClose: () => void; onSaved: () => void }) {
  const [cfg, setCfg] = createSignal<FaceConfig | null>(null);
  createResource(async () => {
    const c = await api<FaceConfig>("/settings/face");
    setCfg(c);
    return c;
  });
  const patch = (p: Partial<FaceConfig>) => setCfg((c) => (c ? { ...c, ...p } : c));

  const save = async () => {
    const c = cfg();
    if (!c) return;
    try {
      await api("/settings/face", { method: "PUT", body: c });
      toast("设置已保存");
      props.onSaved();
      props.onClose();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <Modal title="人脸识别设置" hideOk width={560} onClose={props.onClose}>
      <Show when={cfg()} fallback={<div class="py-8 text-center text-base-content/60">加载中…</div>}>
        <div class="flex flex-col gap-3">
          <label class="flex items-center justify-between">
            <span class="text-sm">启用人脸识别</span>
            <input
              type="checkbox"
              class="toggle toggle-primary"
              checked={cfg()!.enabled}
              onChange={(e) => patch({ enabled: e.currentTarget.checked })}
            />
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-sm">推理服务地址 (sidecar)</span>
            <input
              class="input input-bordered input-sm"
              value={cfg()!.sidecarURL}
              onInput={(e) => patch({ sidecarURL: e.currentTarget.value })}
            />
          </label>
          <div class="grid grid-cols-2 gap-3">
            <NumberField
              label="采样帧率 (fps)"
              value={cfg()!.sampleFps}
              step={0.1}
              onChange={(v) => patch({ sampleFps: v })}
            />
            <NumberField
              label="每段最多帧数"
              value={cfg()!.maxFramesPerJob}
              onChange={(v) => patch({ maxFramesPerJob: v })}
            />
            <NumberField
              label="最小人脸像素"
              value={cfg()!.minFaceSize}
              onChange={(v) => patch({ minFaceSize: v })}
            />
            <NumberField
              label="检测阈值"
              value={cfg()!.detThreshold}
              step={0.05}
              onChange={(v) => patch({ detThreshold: v })}
            />
            <NumberField
              label="匹配阈值 (cosine)"
              value={cfg()!.matchThreshold}
              step={0.05}
              onChange={(v) => patch({ matchThreshold: v })}
            />
            <NumberField
              label="复核阈值 (cosine)"
              value={cfg()!.reviewThreshold}
              step={0.05}
              onChange={(v) => patch({ reviewThreshold: v })}
            />
          </div>
          <label class="flex items-center justify-between">
            <span class="text-sm">仅分析有移动的时段</span>
            <input
              type="checkbox"
              class="toggle toggle-primary"
              checked={cfg()!.motionGated}
              onChange={(e) => patch({ motionGated: e.currentTarget.checked })}
            />
          </label>
          <label class="flex items-center justify-between">
            <span class="text-sm" title="在直播流上实时检测人脸并告警（身份归类仍由录像分析完成）">
              实时人脸告警
            </span>
            <input
              type="checkbox"
              class="toggle toggle-primary"
              checked={cfg()!.realtime}
              onChange={(e) => patch({ realtime: e.currentTarget.checked })}
            />
          </label>
          <Show when={cfg()!.realtime}>
            <NumberField
              label="实时采样帧率 (fps)"
              value={cfg()!.realtimeFps}
              step={0.5}
              onChange={(v) => patch({ realtimeFps: v })}
            />
          </Show>
          <div class="flex justify-end gap-2 mt-2">
            <button class="btn btn-sm" onClick={props.onClose}>
              取消
            </button>
            <button class="btn btn-sm btn-primary" onClick={() => void save()}>
              保存
            </button>
          </div>
        </div>
      </Show>
    </Modal>
  );
}

function NumberField(props: { label: string; value: number; step?: number; onChange: (v: number) => void }) {
  return (
    <label class="flex flex-col gap-1">
      <span class="text-sm">{props.label}</span>
      <input
        type="number"
        step={props.step ?? 1}
        class="input input-bordered input-sm"
        value={props.value}
        onInput={(e) => props.onChange(Number(e.currentTarget.value))}
      />
    </label>
  );
}

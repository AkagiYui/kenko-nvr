import { createFileRoute } from "@tanstack/solid-router";
import { createResource, For, Show, type JSX } from "solid-js";
import { createStore, unwrap } from "solid-js/store";
import { Icon } from "@iconify-icon/solid";
import { api } from "~/lib/api";
import { toast } from "~/components/toast";
import type { ChannelType, NotificationChannel, NotificationConfig } from "~/lib/types";

export const Route = createFileRoute("/_authed/notifications")({
  component: Notifications,
});

function Notifications() {
  const [data] = createResource<NotificationConfig>(() => api("/settings/notifications"));
  return (
    <>
      <h1 class="text-[22px] font-semibold mb-5">通知告警</h1>
      <Show when={data()} fallback={<div class="text-base-content/60">加载中…</div>}>
        {(c) => <NotificationForm initial={c()} />}
      </Show>
    </>
  );
}

const CHANNEL_TYPES: { value: ChannelType; label: string }[] = [
  { value: "email", label: "邮件（SMTP）" },
  { value: "webhook", label: "Webhook" },
  { value: "mqtt", label: "MQTT" },
  { value: "webpush", label: "浏览器推送" },
];

const TYPE_LABEL: Record<ChannelType, string> = {
  email: "邮件（SMTP）",
  webhook: "Webhook",
  mqtt: "MQTT",
  webpush: "浏览器推送",
};

// Notification kinds a channel can subscribe to (empty selection = follow global).
const KINDS = [
  { value: "motion", label: "移动侦测" },
  { value: "offline", label: "摄像头离线" },
];

function newChannel(type: ChannelType): NotificationChannel {
  return {
    id: crypto.randomUUID(),
    name: TYPE_LABEL[type],
    type,
    enabled: true,
    events: [],
    email: { enabled: true, host: "", port: 587, username: "", password: "", from: "", to: "", useTLS: true },
    webhook: { enabled: true, url: "" },
    mqtt: { enabled: true, brokerURL: "", username: "", password: "", topic: "kenko-nvr/events", clientID: "kenko-nvr" },
    subject: "mailto:admin@example.com",
  };
}

function NotificationForm(props: { initial: NotificationConfig }) {
  const [conf, setConf] = createStore<NotificationConfig>({
    ...props.initial,
    channels: props.initial.channels ?? [],
  });

  const save = async () => {
    try {
      await api("/settings/notifications", { method: "PUT", body: unwrap(conf) });
      toast("已保存");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  const addChannel = (type: ChannelType) =>
    setConf("channels", (cs) => [...cs, newChannel(type)]);
  const removeChannel = (i: number) =>
    setConf("channels", (cs) => cs.filter((_, idx) => idx !== i));

  return (
    <div class="grid gap-4">
      <Card title="全局">
        <Check checked={conf.enabled} onChange={(v) => setConf("enabled", v)} label="启用通知（关闭则所有渠道都不发送）" />
        <div class="text-[13px] text-base-content/50 -mt-1">
          下面两项是各渠道的默认通知范围；渠道可在自身设置里覆盖。
        </div>
        <Check checked={conf.onMotion} onChange={(v) => setConf("onMotion", v)} label="默认：检测到移动时通知" />
        <Check checked={conf.onCameraOffline} onChange={(v) => setConf("onCameraOffline", v)} label="默认：摄像头离线时通知" />
        <Labeled label="同一摄像头最小通知间隔（秒）" class="max-w-[260px]">
          <input
            class="input input-bordered w-full"
            type="number"
            value={conf.minIntervalSeconds}
            onInput={(e) => setConf("minIntervalSeconds", parseInt(e.currentTarget.value, 10) || 0)}
          />
        </Labeled>
      </Card>

      <div class="flex items-center justify-between">
        <div class="font-semibold">通知渠道（{conf.channels.length}）</div>
        <div class="dropdown dropdown-end">
          <div tabindex="0" role="button" class="btn btn-sm btn-primary gap-1">
            <Icon icon="lucide:plus" width="15" height="15" />
            添加渠道
          </div>
          <ul tabindex="0" class="dropdown-content menu z-20 mt-1 w-44 rounded-box border border-base-300 bg-base-100 p-1 shadow-lg">
            <For each={CHANNEL_TYPES}>
              {(t) => (
                <li>
                  <a onClick={() => addChannel(t.value)}>{t.label}</a>
                </li>
              )}
            </For>
          </ul>
        </div>
      </div>

      <Show
        when={conf.channels.length}
        fallback={
          <div class="card border border-dashed border-base-300 bg-base-200 py-10 text-center text-base-content/50">
            还没有通知渠道，点击右上角“添加渠道”。
          </div>
        }
      >
        <For each={conf.channels}>
          {(ch, i) => (
            <ChannelCard
              ch={ch}
              setField={(...args: [any, ...any[]]) => (setConf as any)("channels", i(), ...args)}
              config={conf}
              onTest={() => void testChannel(conf, ch.id)}
              onRemove={() => removeChannel(i())}
            />
          )}
        </For>
      </Show>

      <div class="flex gap-2.5">
        <button class="btn btn-primary" onClick={() => void save()}>保存</button>
      </div>
    </div>
  );
}

async function testChannel(conf: NotificationConfig, channelId: string) {
  toast("正在发送测试通知…");
  try {
    await api(`/settings/notifications/test?channelId=${encodeURIComponent(channelId)}`, {
      method: "POST",
      body: unwrap(conf),
    });
    toast("测试通知已发送 ✓");
  } catch (e) {
    toast("失败：" + (e as Error).message, "error");
  }
}

// ChannelCard edits a single channel. setField proxies into conf.channels[i].
function ChannelCard(props: {
  ch: NotificationChannel;
  config: NotificationConfig;
  setField: (...path: [any, ...any[]]) => void;
  onTest: () => void;
  onRemove: () => void;
}) {
  const ch = () => props.ch;
  const toggleEvent = (kind: string) => {
    const evs = ch().events ?? [];
    props.setField("events", evs.includes(kind) ? evs.filter((e) => e !== kind) : [...evs, kind]);
  };

  return (
    <div class="card border border-base-300 bg-base-200">
      <div class="flex flex-wrap items-center gap-2 border-b border-base-300 px-4 py-2.5">
        <input
          type="checkbox"
          class="checkbox checkbox-sm"
          checked={ch().enabled}
          title="启用此渠道"
          onChange={(e) => props.setField("enabled", e.currentTarget.checked)}
        />
        <input
          class="input input-bordered input-sm w-[160px]"
          placeholder="渠道名称"
          value={ch().name}
          onInput={(e) => props.setField("name", e.currentTarget.value)}
        />
        <select
          class="select select-bordered select-sm"
          value={ch().type}
          onChange={(e) => props.setField("type", e.currentTarget.value)}
        >
          <For each={CHANNEL_TYPES}>{(t) => <option value={t.value}>{t.label}</option>}</For>
        </select>
        <div class="flex-1" />
        <button class="btn btn-ghost btn-sm" onClick={props.onTest}>测试</button>
        <button class="btn btn-ghost btn-sm text-error" onClick={props.onRemove} title="删除渠道">
          <Icon icon="lucide:trash-2" width="15" height="15" />
        </button>
      </div>

      <div class="card-body gap-3">
        <div>
          <div class="mb-1.5 text-[13px] text-base-content/60">
            通知类型（不选则跟随全局默认）
          </div>
          <div class="flex flex-wrap gap-4">
            <For each={KINDS}>
              {(k) => (
                <label class="label cursor-pointer justify-start gap-2 py-0">
                  <input
                    type="checkbox"
                    class="checkbox checkbox-sm"
                    checked={(ch().events ?? []).includes(k.value)}
                    onChange={() => toggleEvent(k.value)}
                  />
                  <span class="label-text">{k.label}</span>
                </label>
              )}
            </For>
          </div>
        </div>

        <Show when={ch().type === "email"}>
          <EmailFields ch={ch()} setField={props.setField} />
        </Show>
        <Show when={ch().type === "webhook"}>
          <Labeled label="URL">
            <input
              class="input input-bordered w-full"
              placeholder="https://example.com/hook"
              value={ch().webhook.url}
              onInput={(e) => props.setField("webhook", "url", e.currentTarget.value)}
            />
          </Labeled>
        </Show>
        <Show when={ch().type === "mqtt"}>
          <MqttFields ch={ch()} setField={props.setField} />
        </Show>
        <Show when={ch().type === "webpush"}>
          <WebPushFields ch={ch()} setField={props.setField} />
        </Show>
      </div>
    </div>
  );
}

function EmailFields(props: { ch: NotificationChannel; setField: (...p: [any, ...any[]]) => void }) {
  const e = () => props.ch.email;
  const set = (k: keyof NotificationChannel["email"], v: unknown) => props.setField("email", k, v);
  return (
    <>
      <div class="flex flex-wrap gap-3">
        <Labeled label="SMTP 服务器" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="smtp.example.com" value={e().host} onInput={(ev) => set("host", ev.currentTarget.value)} />
        </Labeled>
        <Labeled label="端口" class="w-[120px]">
          <input class="input input-bordered w-full" type="number" value={e().port} onInput={(ev) => set("port", parseInt(ev.currentTarget.value, 10) || 587)} />
        </Labeled>
      </div>
      <div class="flex flex-wrap gap-3">
        <Labeled label="用户名" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" value={e().username} onInput={(ev) => set("username", ev.currentTarget.value)} />
        </Labeled>
        <Labeled label="密码" class="flex-1 min-w-[200px]">
          <input type="password" class="input input-bordered w-full" placeholder="留空表示不修改" value={e().password} onInput={(ev) => set("password", ev.currentTarget.value)} />
        </Labeled>
      </div>
      <div class="flex flex-wrap gap-3">
        <Labeled label="发件人" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="nvr@example.com" value={e().from} onInput={(ev) => set("from", ev.currentTarget.value)} />
        </Labeled>
        <Labeled label="收件人（逗号分隔）" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" value={e().to} onInput={(ev) => set("to", ev.currentTarget.value)} />
        </Labeled>
      </div>
      <Check checked={e().useTLS} onChange={(v) => set("useTLS", v)} label="使用 TLS（587 用 STARTTLS，465 用隐式 TLS）" />
    </>
  );
}

function MqttFields(props: { ch: NotificationChannel; setField: (...p: [any, ...any[]]) => void }) {
  const m = () => props.ch.mqtt;
  const set = (k: keyof NotificationChannel["mqtt"], v: unknown) => props.setField("mqtt", k, v);
  return (
    <>
      <Labeled label="Broker 地址" hint="tcp://host:1883 或 ssl://host:8883">
        <input class="input input-bordered w-full" placeholder="tcp://broker:1883" value={m().brokerURL} onInput={(e) => set("brokerURL", e.currentTarget.value)} />
      </Labeled>
      <div class="flex flex-wrap gap-3">
        <Labeled label="用户名" class="flex-1 min-w-[160px]">
          <input class="input input-bordered w-full" value={m().username} onInput={(e) => set("username", e.currentTarget.value)} />
        </Labeled>
        <Labeled label="密码" class="flex-1 min-w-[160px]">
          <input type="password" class="input input-bordered w-full" placeholder="留空表示不修改" value={m().password} onInput={(e) => set("password", e.currentTarget.value)} />
        </Labeled>
      </div>
      <div class="flex flex-wrap gap-3">
        <Labeled label="主题" class="flex-1 min-w-[160px]">
          <input class="input input-bordered w-full" value={m().topic} onInput={(e) => set("topic", e.currentTarget.value)} />
        </Labeled>
        <Labeled label="Client ID" class="flex-1 min-w-[160px]">
          <input class="input input-bordered w-full" value={m().clientID} onInput={(e) => set("clientID", e.currentTarget.value)} />
        </Labeled>
      </div>
    </>
  );
}

function WebPushFields(props: { ch: NotificationChannel; setField: (...p: [any, ...any[]]) => void }) {
  return (
    <>
      <Labeled label="联系人（mailto: 或 https URL）" class="max-w-[360px]">
        <input class="input input-bordered w-full" value={props.ch.subject} onInput={(e) => props.setField("subject", e.currentTarget.value)} />
      </Labeled>
      <p class="text-sm text-base-content/60">保存后，点击下方按钮把当前浏览器注册为推送目标（需 HTTPS 或 localhost）。</p>
      <div>
        <button class="btn btn-ghost btn-sm" onClick={() => void subscribePush()}>订阅此浏览器</button>
      </div>
    </>
  );
}

async function subscribePush() {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
    toast("此浏览器不支持推送", "error");
    return;
  }
  try {
    const perm = await Notification.requestPermission();
    if (perm !== "granted") {
      toast("通知权限被拒绝", "error");
      return;
    }
    const reg = await navigator.serviceWorker.register("/sw.js");
    const { publicKey } = await api<{ publicKey: string }>("/notifications/vapid");
    if (!publicKey) {
      toast("服务端未启用 Web Push", "error");
      return;
    }
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(publicKey),
    });
    await api("/notifications/subscribe", { method: "POST", body: sub.toJSON() });
    toast("已订阅浏览器推送 ✓");
  } catch (e) {
    toast("订阅失败：" + (e as Error).message, "error");
  }
}

function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(b64);
  const buf = new ArrayBuffer(raw.length);
  const out = new Uint8Array(buf);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// ---- small shared form bits (mirrors settings.tsx) ----

function Card(props: { title: string; children: JSX.Element }) {
  return (
    <div class="card bg-base-200 border border-base-300">
      <div class="px-4 py-3 border-b border-base-300 font-semibold">{props.title}</div>
      <div class="card-body gap-3">{props.children}</div>
    </div>
  );
}

function Check(props: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label class="label cursor-pointer justify-start gap-2">
      <input type="checkbox" class="checkbox checkbox-sm" checked={props.checked} onChange={(e) => props.onChange(e.currentTarget.checked)} />
      <span class="label-text">{props.label}</span>
    </label>
  );
}

function Labeled(props: { label: string; hint?: string; class?: string; children: JSX.Element }) {
  return (
    <div class={props.class}>
      <label class="label">
        <span class="label-text">{props.label}</span>
        <Show when={props.hint}>
          <span class="label-text-alt text-base-content/40">{props.hint}</span>
        </Show>
      </label>
      {props.children}
    </div>
  );
}

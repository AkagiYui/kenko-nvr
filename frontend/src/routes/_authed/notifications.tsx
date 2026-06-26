import { createFileRoute } from "@tanstack/solid-router";
import { createResource, createSignal, Show, type JSX } from "solid-js";
import { api } from "~/lib/api";
import { toast } from "~/components/toast";
import type { NotificationConfig } from "~/lib/types";

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

function NotificationForm(props: { initial: NotificationConfig }) {
  const c = props.initial;
  const [enabled, setEnabled] = createSignal(c.enabled);
  const [onMotion, setOnMotion] = createSignal(c.onMotion);
  const [onOffline, setOnOffline] = createSignal(c.onCameraOffline);
  const [minInterval, setMinInterval] = createSignal(String(c.minIntervalSeconds ?? 60));

  // Email
  const [emEnabled, setEmEnabled] = createSignal(c.email.enabled);
  const [emHost, setEmHost] = createSignal(c.email.host ?? "");
  const [emPort, setEmPort] = createSignal(String(c.email.port || 587));
  const [emUser, setEmUser] = createSignal(c.email.username ?? "");
  const [emPass, setEmPass] = createSignal("");
  const [emFrom, setEmFrom] = createSignal(c.email.from ?? "");
  const [emTo, setEmTo] = createSignal(c.email.to ?? "");
  const [emTLS, setEmTLS] = createSignal(c.email.useTLS);

  // Webhook
  const [whEnabled, setWhEnabled] = createSignal(c.webhook.enabled);
  const [whURL, setWhURL] = createSignal(c.webhook.url ?? "");

  // MQTT
  const [mqEnabled, setMqEnabled] = createSignal(c.mqtt.enabled);
  const [mqBroker, setMqBroker] = createSignal(c.mqtt.brokerURL ?? "");
  const [mqUser, setMqUser] = createSignal(c.mqtt.username ?? "");
  const [mqPass, setMqPass] = createSignal("");
  const [mqTopic, setMqTopic] = createSignal(c.mqtt.topic ?? "kenko-nvr/events");
  const [mqClient, setMqClient] = createSignal(c.mqtt.clientID ?? "kenko-nvr");

  // Web push
  const [wpEnabled, setWpEnabled] = createSignal(c.webPush.enabled);
  const [wpSubject, setWpSubject] = createSignal(c.webPush.subject ?? "mailto:admin@example.com");

  const collect = (): NotificationConfig => ({
    enabled: enabled(),
    onMotion: onMotion(),
    onCameraOffline: onOffline(),
    minIntervalSeconds: parseInt(minInterval(), 10) || 0,
    email: {
      enabled: emEnabled(),
      host: emHost(),
      port: parseInt(emPort(), 10) || 587,
      username: emUser(),
      password: emPass(),
      from: emFrom(),
      to: emTo(),
      useTLS: emTLS(),
    },
    webhook: { enabled: whEnabled(), url: whURL() },
    mqtt: {
      enabled: mqEnabled(),
      brokerURL: mqBroker(),
      username: mqUser(),
      password: mqPass(),
      topic: mqTopic(),
      clientID: mqClient(),
    },
    webPush: { enabled: wpEnabled(), subject: wpSubject() },
  });

  const save = async () => {
    try {
      await api("/settings/notifications", { method: "PUT", body: collect() });
      toast("已保存");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };
  const test = async () => {
    toast("正在发送测试通知…");
    try {
      await api("/settings/notifications/test", { method: "POST", body: collect() });
      toast("测试通知已发送 ✓");
    } catch (e) {
      toast("失败：" + (e as Error).message, "error");
    }
  };

  return (
    <div class="grid gap-4">
      <Card title="全局">
        <Check checked={enabled()} onChange={setEnabled} label="启用通知（关闭则所有渠道都不发送）" />
        <Check checked={onMotion()} onChange={setOnMotion} label="检测到移动时通知" />
        <Check checked={onOffline()} onChange={setOnOffline} label="摄像头离线时通知" />
        <Labeled label="同一摄像头最小通知间隔（秒）" class="max-w-[260px]">
          <input class="input input-bordered w-full" type="number" value={minInterval()} onInput={(e) => setMinInterval(e.currentTarget.value)} />
        </Labeled>
      </Card>

      <Card title="邮件（SMTP）">
        <Check checked={emEnabled()} onChange={setEmEnabled} label="启用邮件通知" />
        <div class="flex gap-3 flex-wrap">
          <Labeled label="SMTP 服务器" class="flex-1 min-w-[200px]">
            <input class="input input-bordered w-full" placeholder="smtp.example.com" value={emHost()} onInput={(e) => setEmHost(e.currentTarget.value)} />
          </Labeled>
          <Labeled label="端口" class="w-[120px]">
            <input class="input input-bordered w-full" type="number" value={emPort()} onInput={(e) => setEmPort(e.currentTarget.value)} />
          </Labeled>
        </div>
        <div class="flex gap-3 flex-wrap">
          <Labeled label="用户名" class="flex-1 min-w-[200px]">
            <input class="input input-bordered w-full" value={emUser()} onInput={(e) => setEmUser(e.currentTarget.value)} />
          </Labeled>
          <Labeled label="密码" class="flex-1 min-w-[200px]">
            <input type="password" class="input input-bordered w-full" placeholder="留空表示不修改" value={emPass()} onInput={(e) => setEmPass(e.currentTarget.value)} />
          </Labeled>
        </div>
        <div class="flex gap-3 flex-wrap">
          <Labeled label="发件人" class="flex-1 min-w-[200px]">
            <input class="input input-bordered w-full" placeholder="nvr@example.com" value={emFrom()} onInput={(e) => setEmFrom(e.currentTarget.value)} />
          </Labeled>
          <Labeled label="收件人（逗号分隔）" class="flex-1 min-w-[200px]">
            <input class="input input-bordered w-full" value={emTo()} onInput={(e) => setEmTo(e.currentTarget.value)} />
          </Labeled>
        </div>
        <Check checked={emTLS()} onChange={setEmTLS} label="使用 TLS（587 用 STARTTLS，465 用隐式 TLS）" />
      </Card>

      <Card title="Webhook">
        <Check checked={whEnabled()} onChange={setWhEnabled} label="启用 Webhook（POST JSON）" />
        <Labeled label="URL">
          <input class="input input-bordered w-full" placeholder="https://example.com/hook" value={whURL()} onInput={(e) => setWhURL(e.currentTarget.value)} />
        </Labeled>
      </Card>

      <Card title="MQTT">
        <Check checked={mqEnabled()} onChange={setMqEnabled} label="启用 MQTT 发布" />
        <Labeled label="Broker 地址" hint="tcp://host:1883 或 ssl://host:8883">
          <input class="input input-bordered w-full" placeholder="tcp://broker:1883" value={mqBroker()} onInput={(e) => setMqBroker(e.currentTarget.value)} />
        </Labeled>
        <div class="flex gap-3 flex-wrap">
          <Labeled label="用户名" class="flex-1 min-w-[160px]">
            <input class="input input-bordered w-full" value={mqUser()} onInput={(e) => setMqUser(e.currentTarget.value)} />
          </Labeled>
          <Labeled label="密码" class="flex-1 min-w-[160px]">
            <input type="password" class="input input-bordered w-full" placeholder="留空表示不修改" value={mqPass()} onInput={(e) => setMqPass(e.currentTarget.value)} />
          </Labeled>
        </div>
        <div class="flex gap-3 flex-wrap">
          <Labeled label="主题" class="flex-1 min-w-[160px]">
            <input class="input input-bordered w-full" value={mqTopic()} onInput={(e) => setMqTopic(e.currentTarget.value)} />
          </Labeled>
          <Labeled label="Client ID" class="flex-1 min-w-[160px]">
            <input class="input input-bordered w-full" value={mqClient()} onInput={(e) => setMqClient(e.currentTarget.value)} />
          </Labeled>
        </div>
      </Card>

      <Card title="浏览器推送（Web Push）">
        <Check checked={wpEnabled()} onChange={setWpEnabled} label="启用浏览器推送" />
        <Labeled label="联系人（mailto: 或 https URL）" class="max-w-[360px]">
          <input class="input input-bordered w-full" value={wpSubject()} onInput={(e) => setWpSubject(e.currentTarget.value)} />
        </Labeled>
        <p class="text-sm text-base-content/60">保存并启用后，点击下方按钮把当前浏览器注册为推送目标（需 HTTPS 或 localhost）。</p>
        <div>
          <button class="btn btn-ghost btn-sm" onClick={() => void subscribePush()}>
            订阅此浏览器
          </button>
        </div>
      </Card>

      <div class="flex gap-2.5">
        <button class="btn btn-primary" onClick={() => void save()}>保存</button>
        <button class="btn btn-ghost" onClick={() => void test()}>发送测试通知</button>
      </div>
    </div>
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

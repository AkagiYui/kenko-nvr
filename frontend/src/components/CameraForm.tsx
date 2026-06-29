import { createResource, createSignal, For, Show } from "solid-js";
import { Modal } from "./Modal";
import { toast } from "./toast";
import { api } from "~/lib/api";
import type {
  Camera,
  CameraInput,
  GB28181Device,
  GB28181Info,
  OnvifProbeResult,
  OnvifProfile,
  SourceType,
} from "~/lib/types";

interface Props {
  camera: Camera | null;
  seed?: Partial<CameraInput>;
  onClose: () => void;
  onSaved: () => void;
}

// CameraForm is the add/edit modal, replicating the original cameraForm: the
// visible sections track the source type, an ONVIF source implies ONVIF control,
// and "探测 ONVIF" fills the RTSP address from the device.
export function CameraForm(props: Props) {
  const c = props.camera;
  const seed = props.seed ?? {};

  const [name, setName] = createSignal(c?.name ?? seed.name ?? "");
  const [sourceType, setSourceType] = createSignal<SourceType>(c?.sourceType ?? seed.sourceType ?? "rtsp");
  const [transport, setTransport] = createSignal(c?.transport ?? seed.transport ?? "");
  const [url, setUrl] = createSignal(c?.url ?? seed.url ?? "");
  const [username, setUsername] = createSignal(c?.username ?? seed.username ?? "");
  const [password, setPassword] = createSignal("");
  const [record, setRecord] = createSignal(c?.record ?? seed.record ?? false);
  const [enabled, setEnabled] = createSignal(c?.enabled ?? seed.enabled ?? true);
  const [motionEnabled, setMotionEnabled] = createSignal(c?.motionEnabled ?? seed.motionEnabled ?? false);
  const [recordMode, setRecordMode] = createSignal<string>(c?.recordMode ?? seed.recordMode ?? "continuous");
  const [motionSensitivity, setMotionSensitivity] = createSignal(c?.motionSensitivity ?? seed.motionSensitivity ?? 50);
  const [onvifEnabled, setOnvifEnabled] = createSignal(c?.onvifEnabled ?? seed.onvifEnabled ?? false);
  const [onvifXAddr, setOnvifXAddr] = createSignal(c?.onvifXAddr ?? seed.onvifXAddr ?? "");
  const [onvifUsername, setOnvifUsername] = createSignal(c?.onvifUsername ?? seed.onvifUsername ?? "");
  const [onvifPassword, setOnvifPassword] = createSignal("");
  const [onvifProfile, setOnvifProfile] = createSignal(c?.onvifProfile ?? seed.onvifProfile ?? "");
  // Profiles discovered by the last ONVIF probe; the token can be picked from
  // these or typed manually (custom mode).
  const [profiles, setProfiles] = createSignal<OnvifProfile[]>([]);
  const [profileCustom, setProfileCustom] = createSignal(false);
  const [probing, setProbing] = createSignal(false);
  const [gbDeviceId, setGbDeviceId] = createSignal(c?.gb28181DeviceId ?? seed.gb28181DeviceId ?? "");
  const [gbChannelId, setGbChannelId] = createSignal(c?.gb28181ChannelId ?? seed.gb28181ChannelId ?? "");

  const isGB = () => sourceType() === "gb28181";
  const [gbInfo] = createResource(
    () => (isGB() ? "info" : null),
    () => api<GB28181Info>("/gb28181/info"),
  );
  const [gbDevices, { refetch: refetchDevices }] = createResource(
    () => (isGB() ? "devices" : null),
    () => api<GB28181Device[]>("/gb28181/devices"),
  );
  const gbChannels = () => gbDevices()?.find((d) => d.id === gbDeviceId())?.channels ?? [];
  const refreshGB = async () => {
    const id = gbDeviceId();
    if (id) {
      try {
        await api(`/gb28181/devices/${id}/refresh`, { method: "POST" });
      } catch {
        /* ignore */
      }
    }
    setTimeout(() => void refetchDevices(), 700);
    void refetchDevices();
  };

  const isOnvifSource = () => sourceType() === "onvif";
  const showOnvifSection = () => isOnvifSource() || onvifEnabled();
  const pwPlaceholder = c ? "（留空表示不修改）" : "";

  const onSourceChange = (v: SourceType) => {
    setSourceType(v);
    if (v === "onvif") setOnvifEnabled(true); // ONVIF source implies control
  };

  const probe = async () => {
    if (!onvifXAddr()) return toast("请先填写 ONVIF 设备地址", "error");
    setProbing(true);
    toast("正在探测…");
    try {
      const res = await api<OnvifProbeResult>("/onvif/probe", {
        method: "POST",
        body: { xaddr: onvifXAddr(), username: onvifUsername(), password: onvifPassword() },
      });
      const ps = res.profiles ?? [];
      setProfiles(ps);
      if (ps.length === 0) return toast("未发现 profile", "error");
      // If the saved token isn't one of the discovered profiles, keep it as a
      // manual entry; otherwise resolve the stream for the selected/first one.
      const matched = ps.find((p) => p.token === onvifProfile());
      setProfileCustom(!!onvifProfile() && !matched);
      const chosen = matched ?? ps[0];
      if (!matched) setOnvifProfile(chosen.token);
      if (chosen.streamUri) setUrl(chosen.streamUri);
      toast(`发现 ${ps.length} 个 profile（${res.info?.manufacturer ?? ""} ${res.info?.model ?? ""}）`);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      setProbing(false);
    }
  };

  const save = async () => {
    const payload: CameraInput = {
      name: name(),
      sourceType: sourceType(),
      url: url() || "",
      username: username() || "",
      password: password() || "",
      transport: transport() || "",
      record: record(),
      enabled: enabled(),
      onvifEnabled: onvifEnabled(),
      onvifXAddr: onvifXAddr() || "",
      onvifUsername: onvifUsername() || "",
      onvifPassword: onvifPassword() || "",
      onvifProfile: onvifProfile() || "",
      motionEnabled: motionEnabled() || recordMode() === "motion",
      recordMode: recordMode() as "continuous" | "motion",
      motionSensitivity: motionSensitivity(),
      gb28181DeviceId: gbDeviceId() || "",
      gb28181ChannelId: gbChannelId() || "",
    };
    if (c) await api(`/cameras/${c.id}`, { method: "PUT", body: payload });
    else await api("/cameras", { method: "POST", body: payload });
    toast("已保存");
    props.onSaved();
  };

  return (
    <Modal title={c ? "编辑摄像头" : "添加摄像头"} width={560} onOk={save} onClose={props.onClose}>
      <div class="space-y-3">
        <Field label="名称">
          <input class="input input-bordered w-full" value={name()} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>

        <div class="flex gap-3">
          <Field label="来源类型" class="flex-1">
            <select
              class="select select-bordered w-full"
              value={sourceType()}
              onChange={(e) => onSourceChange(e.currentTarget.value as SourceType)}
            >
              <option value="rtsp">RTSP 拉流</option>
              <option value="onvif">ONVIF（自动获取视频流 + 云台）</option>
              <option value="rtmp">RTMP 推流（设备推到本机）</option>
              <option value="gb28181">GB28181 国标接入</option>
            </select>
          </Field>
          <Field label="RTSP 传输" class="flex-1">
            <select
              class="select select-bordered w-full"
              value={transport()}
              onChange={(e) => setTransport(e.currentTarget.value)}
            >
              <option value="">自动</option>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </select>
          </Field>
        </div>

        <Show when={sourceType() === "rtsp"}>
          <div>
            <Field label="RTSP 地址" hint="rtsp://host:554/stream">
              <input
                class="input input-bordered w-full"
                placeholder="rtsp://..."
                value={url()}
                onInput={(e) => setUrl(e.currentTarget.value)}
              />
            </Field>
            <div class="flex gap-3 mt-1">
              <Field label="用户名" class="flex-1">
                <input class="input input-bordered w-full" value={username()} onInput={(e) => setUsername(e.currentTarget.value)} />
              </Field>
              <Field label="密码" class="flex-1">
                <input
                  type="password"
                  class="input input-bordered w-full"
                  placeholder={pwPlaceholder}
                  value={password()}
                  onInput={(e) => setPassword(e.currentTarget.value)}
                />
              </Field>
            </div>
          </div>
        </Show>

        <Show when={sourceType() === "rtmp"}>
          <p class="text-sm text-base-content/60">
            设备/编码器请推流到：
            <code class="ml-1">rtmp://&lt;本机IP&gt;:1935/live/{c?.id ?? "<保存后生成的ID>"}</code>
          </p>
        </Show>

        <Show when={sourceType() === "onvif"}>
          <p class="text-sm text-base-content/60">视频流地址将通过 ONVIF 自动获取（设备地址与账号在下方填写）。</p>
        </Show>

        <Show when={isGB()}>
          <div class="space-y-2 rounded-md bg-base-300/40 p-3">
            <Show
              when={gbInfo()?.enabled}
              fallback={
                <p class="text-sm text-error">
                  GB28181 服务未启用。请在 config.yaml 中设置 <code>gb28181.enabled: true</code> 并重启。
                </p>
              }
            >
              <p class="text-xs text-base-content/60 leading-relaxed">
                在设备/NVR 的国标参数中填写 —— SIP 服务器编号：<code>{gbInfo()?.serverId}</code>，SIP 域：
                <code>{gbInfo()?.domain}</code>，SIP 服务器地址：
                <code>{gbInfo()?.mediaIp}{gbInfo()?.sipAddr}</code>。设备注册成功后会出现在下方。
              </p>
            </Show>

            <div class="flex gap-2 items-end">
              <Field label="国标设备" class="flex-1">
                <select
                  class="select select-bordered w-full"
                  value={gbDeviceId()}
                  onChange={(e) => {
                    setGbDeviceId(e.currentTarget.value);
                    setGbChannelId("");
                  }}
                >
                  <option value="">（选择已注册设备）</option>
                  <For each={gbDevices()}>
                    {(d) => (
                      <option value={d.id}>
                        {(d.name || d.id) + (d.online ? "" : "（离线）")}
                      </option>
                    )}
                  </For>
                </select>
              </Field>
              <button class="btn btn-sm btn-ghost" onClick={() => void refreshGB()}>
                刷新
              </button>
            </div>

            <Show when={gbChannels().length > 0}>
              <Field label="通道" hint="单通道设备可留空（默认用设备本身）">
                <select
                  class="select select-bordered w-full"
                  value={gbChannelId()}
                  onChange={(e) => setGbChannelId(e.currentTarget.value)}
                >
                  <option value="">（设备本身 / 默认通道）</option>
                  <For each={gbChannels()}>
                    {(ch) => <option value={ch.id}>{ch.name || ch.id}</option>}
                  </For>
                </select>
              </Field>
            </Show>

            <Field label="设备编号（手动）" hint="设备未自动出现时，可手动填写 20 位国标编号">
              <input
                class="input input-bordered w-full"
                placeholder="34020000001320000001"
                value={gbDeviceId()}
                onInput={(e) => setGbDeviceId(e.currentTarget.value)}
              />
            </Field>
          </div>
        </Show>

        <label class="label cursor-pointer justify-start gap-2">
          <input type="checkbox" class="checkbox checkbox-sm" checked={record()} onChange={(e) => setRecord(e.currentTarget.checked)} />
          <span class="label-text">启用录像</span>
        </label>
        <label class="label cursor-pointer justify-start gap-2">
          <input type="checkbox" class="checkbox checkbox-sm" checked={enabled()} onChange={(e) => setEnabled(e.currentTarget.checked)} />
          <span class="label-text">启用此摄像头</span>
        </label>

        <div class="divider my-1">移动侦测</div>

        <label class="label cursor-pointer justify-start gap-2">
          <input
            type="checkbox"
            class="checkbox checkbox-sm"
            checked={motionEnabled()}
            onChange={(e) => setMotionEnabled(e.currentTarget.checked)}
          />
          <span class="label-text">启用移动侦测（需 ffmpeg；产生事件并可触发告警）</span>
        </label>

        <Field label="录像模式" hint="事件触发仅在检测到移动时录像">
          <select
            class="select select-bordered w-full"
            value={recordMode()}
            onChange={(e) => setRecordMode(e.currentTarget.value)}
          >
            <option value="continuous">持续录像</option>
            <option value="motion">移动事件触发录像</option>
          </select>
        </Field>

        <Show when={motionEnabled() || recordMode() === "motion"}>
          <Field label={`灵敏度：${motionSensitivity()}（越高越灵敏）`}>
            <input
              type="range"
              min="0"
              max="100"
              class="range range-sm"
              value={motionSensitivity()}
              onInput={(e) => setMotionSensitivity(parseInt(e.currentTarget.value, 10))}
            />
          </Field>
        </Show>

        <div class="divider my-1" />

        <Show when={!isOnvifSource()}>
          <label class="label cursor-pointer justify-start gap-2">
            <input
              type="checkbox"
              class="checkbox checkbox-sm"
              checked={onvifEnabled()}
              onChange={(e) => setOnvifEnabled(e.currentTarget.checked)}
            />
            <span class="label-text">启用 ONVIF 控制（云台 PTZ）</span>
          </label>
        </Show>

        <Show when={showOnvifSection()}>
          <div>
            <Field label="ONVIF 设备地址" hint="填 host 或 host:port（粘贴发现的完整 URL 也可）">
              <input
                class="input input-bordered w-full"
                placeholder="192.168.5.19"
                value={onvifXAddr()}
                onInput={(e) => setOnvifXAddr(e.currentTarget.value)}
              />
            </Field>
            <div class="flex gap-3 mt-1">
              <Field label="ONVIF 用户名" class="flex-1">
                <input class="input input-bordered w-full" value={onvifUsername()} onInput={(e) => setOnvifUsername(e.currentTarget.value)} />
              </Field>
              <Field label="ONVIF 密码" class="flex-1">
                <input
                  type="password"
                  class="input input-bordered w-full"
                  placeholder={pwPlaceholder}
                  value={onvifPassword()}
                  onInput={(e) => setOnvifPassword(e.currentTarget.value)}
                />
              </Field>
            </div>
            <Field label="Profile Token" hint="探测后可下拉选择，或选“自定义”手动填写；留空用第一个">
              <Show
                when={profiles().length > 0}
                fallback={
                  <input
                    class="input input-bordered w-full"
                    placeholder="留空则自动使用第一个"
                    value={onvifProfile()}
                    onInput={(e) => setOnvifProfile(e.currentTarget.value)}
                  />
                }
              >
                <select
                  class="select select-bordered w-full"
                  value={
                    profileCustom()
                      ? "__custom__"
                      : profiles().some((p) => p.token === onvifProfile())
                        ? onvifProfile()
                        : ""
                  }
                  onChange={(e) => {
                    const v = e.currentTarget.value;
                    if (v === "__custom__") {
                      setProfileCustom(true);
                      return;
                    }
                    setProfileCustom(false);
                    setOnvifProfile(v);
                    const p = profiles().find((p) => p.token === v);
                    if (p?.streamUri) setUrl(p.streamUri);
                  }}
                >
                  <option value="">（自动：使用第一个）</option>
                  <For each={profiles()}>
                    {(p) => <option value={p.token}>{(p.name ? p.name + " · " : "") + p.token}</option>}
                  </For>
                  <option value="__custom__">自定义（手动输入）…</option>
                </select>
                <Show when={profileCustom()}>
                  <input
                    class="input input-bordered w-full mt-1.5"
                    placeholder="输入 Profile Token"
                    value={onvifProfile()}
                    onInput={(e) => setOnvifProfile(e.currentTarget.value)}
                  />
                </Show>
              </Show>
            </Field>
            <button class="btn btn-sm btn-ghost mt-2" disabled={probing()} onClick={() => void probe()}>
              探测 ONVIF（获取 RTSP 地址 / Profile）
            </button>
          </div>
        </Show>
      </div>
    </Modal>
  );
}

function Field(props: { label: string; hint?: string; class?: string; children: import("solid-js").JSX.Element }) {
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

import { createFileRoute } from "@tanstack/solid-router";
import { createResource, createSignal, Show, type JSX } from "solid-js";
import { createStore, unwrap } from "solid-js/store";
import { api } from "~/lib/api";
import { toast } from "~/components/toast";
import type {
  GB28181Info,
  HAConfig,
  RecordingConfig,
  RetentionPolicy,
  S3Config,
  SystemConfig,
} from "~/lib/types";

export const Route = createFileRoute("/_authed/settings")({
  component: Settings,
});

function Settings() {
  return (
    <>
      <h1 class="text-[22px] font-semibold mb-5">系统设置</h1>
      <div class="grid gap-4">
        <SystemCard />
        <RecordingCard />
        <RetentionCard />
        <S3Card />
        <HACard />
        <GB28181Card />
      </div>
    </>
  );
}

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
      <input
        type="checkbox"
        class="checkbox checkbox-sm"
        checked={props.checked}
        onChange={(e) => props.onChange(e.currentTarget.checked)}
      />
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

// ---- System / network services (runtime-editable) ----

function SystemCard() {
  const [data] = createResource<SystemConfig>(() => api("/settings/system"));
  return (
    <Show
      when={data()}
      fallback={
        <Card title="网络服务（运行时）">
          <div class="text-base-content/60">加载中…</div>
        </Card>
      }
    >
      {(c) => <SystemForm initial={c()} />}
    </Show>
  );
}

function SystemForm(props: { initial: SystemConfig }) {
  const [conf, setConf] = createStore<SystemConfig>(props.initial);
  const [stun, setStun] = createSignal((props.initial.webrtc.stunServers ?? []).join("\n"));

  const save = async () => {
    const payload: SystemConfig = JSON.parse(JSON.stringify(unwrap(conf)));
    payload.webrtc.stunServers = stun()
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      await api("/settings/system", { method: "PUT", body: payload });
      toast("已保存并应用（相关服务已按需重启）");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <Card title="网络服务（运行时可改，保存即生效）">
      <p class="text-[13px] text-base-content/50">
        修改后无需重启进程，会自动重启对应服务。Web 登录账号请在「用户管理」中修改。
      </p>

      <div class="divider my-0 text-xs">RTMP 推流接入</div>
      <Check
        checked={conf.rtmp.enabled}
        onChange={(v) => setConf("rtmp", "enabled", v)}
        label="启用 RTMP 接入（编码器/摄像头推流到本机）"
      />
      <Labeled label="监听地址" class="max-w-[220px]">
        <input
          class="input input-bordered w-full"
          placeholder=":1935"
          value={conf.rtmp.addr}
          onInput={(e) => setConf("rtmp", "addr", e.currentTarget.value)}
        />
      </Labeled>

      <div class="divider my-0 text-xs">RTSP 拉流默认传输</div>
      <Labeled label="默认传输方式" class="max-w-[220px]">
        <select
          class="select select-bordered w-full"
          value={conf.rtsp.transport}
          onChange={(e) => setConf("rtsp", "transport", e.currentTarget.value)}
        >
          <option value="automatic">自动</option>
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
        </select>
      </Labeled>

      <div class="divider my-0 text-xs">RTSP 转发服务</div>
      <Check
        checked={conf.rtspServer.enabled}
        onChange={(v) => setConf("rtspServer", "enabled", v)}
        label="启用 RTSP 转发（外部可拉 rtsp://本机:addr/<cameraID>）"
      />
      <Labeled label="监听地址" class="max-w-[220px]">
        <input
          class="input input-bordered w-full"
          placeholder=":8554"
          value={conf.rtspServer.addr}
          onInput={(e) => setConf("rtspServer", "addr", e.currentTarget.value)}
        />
      </Labeled>

      <div class="divider my-0 text-xs">WebRTC 低延迟直播</div>
      <Check
        checked={conf.webrtc.enabled}
        onChange={(v) => setConf("webrtc", "enabled", v)}
        label="启用 WebRTC（WHEP）直播"
      />
      <Labeled label="STUN 服务器（每行一个，跨网络访问时用）" hint="如 stun:stun.l.google.com:19302">
        <textarea
          class="textarea textarea-bordered w-full"
          rows="2"
          value={stun()}
          onInput={(e) => setStun(e.currentTarget.value)}
        />
      </Labeled>

      <div class="divider my-0 text-xs">直播转码</div>
      <p class="text-[13px] text-base-content/50">
        把非 H.264（如 H.265）摄像头实时转码成浏览器可直接播放的画面，仅影响“直播”路径；
        录像始终保存原始码流、不受影响。每台摄像头共用一个 FFmpeg 进程，且仅在有人观看时运行。
        改动对“新开始”的转码生效，正在播放的会话会在重连后采用新设置。
      </p>
      <div class="flex flex-wrap gap-3">
        <Labeled
          label="硬件加速 / 编码器"
          hint="auto 自动探测可用硬件编码，失败回退软件"
          class="flex-1 min-w-[220px]"
        >
          <input
            class="input input-bordered w-full"
            placeholder="auto"
            value={conf.transcode.hwaccel}
            onInput={(e) => setConf("transcode", "hwaccel", e.currentTarget.value)}
          />
        </Labeled>
      </div>
      <p class="text-[12px] text-base-content/40 -mt-1">
        可填：<code>auto</code>（推荐）、<code>none</code>/<code>software</code>（强制软件 libx264）、
        或具体编码器名如 <code>h264_videotoolbox</code>（macOS）、<code>h264_nvenc</code>（NVIDIA）、
        <code>h264_qsv</code>（Intel）、<code>h264_vaapi</code>（Linux）。无效名会自动回退软件。
      </p>
      <div class="flex flex-wrap gap-3">
        <Labeled label="直播码率（kbit/s）" class="flex-1 min-w-[160px]">
          <input
            type="number"
            class="input input-bordered w-full"
            value={conf.transcode.liveBitrateKbps}
            onInput={(e) => setConf("transcode", "liveBitrateKbps", parseInt(e.currentTarget.value, 10) || 0)}
          />
        </Labeled>
        <Labeled label="关键帧间隔 GOP（帧）" hint="越小起播越快；25fps 下 50≈2 秒" class="flex-1 min-w-[160px]">
          <input
            type="number"
            class="input input-bordered w-full"
            value={conf.transcode.liveGop}
            onInput={(e) => setConf("transcode", "liveGop", parseInt(e.currentTarget.value, 10) || 0)}
          />
        </Labeled>
      </div>

      <div class="divider my-0 text-xs">GB28181 国标接入</div>
      <Check
        checked={conf.gb28181.enabled}
        onChange={(v) => setConf("gb28181", "enabled", v)}
        label="启用 GB28181 SIP 平台"
      />
      <div class="flex gap-3 flex-wrap">
        <Labeled label="SIP 监听地址" class="flex-1 min-w-[140px]">
          <input
            class="input input-bordered w-full"
            placeholder=":5060"
            value={conf.gb28181.sipAddr}
            onInput={(e) => setConf("gb28181", "sipAddr", e.currentTarget.value)}
          />
        </Labeled>
        <Labeled label="平台编号 server_id" class="flex-1 min-w-[180px]">
          <input
            class="input input-bordered w-full"
            value={conf.gb28181.serverId}
            onInput={(e) => setConf("gb28181", "serverId", e.currentTarget.value)}
          />
        </Labeled>
      </div>
      <div class="flex gap-3 flex-wrap">
        <Labeled label="SIP 域 domain" class="flex-1 min-w-[140px]">
          <input
            class="input input-bordered w-full"
            value={conf.gb28181.domain}
            onInput={(e) => setConf("gb28181", "domain", e.currentTarget.value)}
          />
        </Labeled>
        <Labeled label="注册密码" class="flex-1 min-w-[140px]">
          <input
            type="password"
            class="input input-bordered w-full"
            placeholder="留空表示不修改"
            value={conf.gb28181.password}
            onInput={(e) => setConf("gb28181", "password", e.currentTarget.value)}
          />
        </Labeled>
      </div>
      <div class="flex gap-3 flex-wrap">
        <Labeled label="媒体 IP（留空自动探测）" class="flex-1 min-w-[140px]">
          <input
            class="input input-bordered w-full"
            value={conf.gb28181.mediaIp}
            onInput={(e) => setConf("gb28181", "mediaIp", e.currentTarget.value)}
          />
        </Labeled>
        <Labeled label="媒体端口范围">
          <div class="flex items-center gap-1">
            <input
              type="number"
              class="input input-bordered w-[100px]"
              value={conf.gb28181.mediaPortMin}
              onInput={(e) => setConf("gb28181", "mediaPortMin", parseInt(e.currentTarget.value, 10) || 0)}
            />
            <span>-</span>
            <input
              type="number"
              class="input input-bordered w-[100px]"
              value={conf.gb28181.mediaPortMax}
              onInput={(e) => setConf("gb28181", "mediaPortMax", parseInt(e.currentTarget.value, 10) || 0)}
            />
          </div>
        </Labeled>
      </div>

      <div>
        <button class="btn btn-primary" onClick={() => void save()}>
          保存并应用
        </button>
      </div>
    </Card>
  );
}

// ---- Recording ----

function RecordingCard() {
  const [data] = createResource<RecordingConfig>(() => api("/settings/recording"));
  return (
    <Show when={data()}>{(c) => <RecordingForm initial={c()} />}</Show>
  );
}

function RecordingForm(props: { initial: RecordingConfig }) {
  const c = props.initial;
  const [segmentSeconds, setSeg] = createSignal(String(c.segmentSeconds));
  const [pathTemplate, setPath] = createSignal(c.pathTemplate);
  const [alignToClock, setAlign] = createSignal(c.alignToClock);
  const [transcode, setTranscode] = createSignal(c.transcode);
  const [videoCodec, setCodec] = createSignal(c.transcodeVideoCodec === "hevc" ? "hevc" : "h264");
  const [crf, setCrf] = createSignal(String(c.transcodeCRF || 23));
  const [preset, setPreset] = createSignal(c.transcodePreset || "fast");

  const save = async () => {
    try {
      await api("/settings/recording", {
        method: "PUT",
        body: {
          segmentSeconds: parseInt(segmentSeconds(), 10) || 600,
          pathTemplate: pathTemplate(),
          alignToClock: alignToClock(),
          transcode: transcode(),
          transcodeVideoCodec: videoCodec(),
          transcodeCRF: parseInt(crf(), 10) || 23,
          transcodePreset: preset() || "fast",
        },
      });
      toast("已保存（下个录像分段生效）");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <Card title="录像设置">
      <Labeled label="单文件时长（秒）" class="max-w-[220px]">
        <input class="input input-bordered w-full" type="number" value={segmentSeconds()} onInput={(e) => setSeg(e.currentTarget.value)} />
      </Labeled>
      <Check checked={alignToClock()} onChange={setAlign} label="按整点对齐切片（如 10 分钟分段从 :00 :10 :20 开始）" />
      <Check checked={transcode()} onChange={setTranscode} label="转码录像（需 ffmpeg；可统一为 H.264、精确对齐切片，更耗 CPU）" />
      <Show when={transcode()}>
        <div class="flex gap-3 flex-wrap">
          <Labeled label="视频编码" class="flex-1 min-w-[160px]">
            <select class="select select-bordered w-full" value={videoCodec()} onChange={(e) => setCodec(e.currentTarget.value)}>
              <option value="h264">H.264（浏览器通用）</option>
              <option value="hevc">H.265 / HEVC</option>
            </select>
          </Labeled>
          <Labeled label="CRF（0-51，越小越好）" class="flex-1 min-w-[160px]">
            <input class="input input-bordered w-full" type="number" value={crf()} onInput={(e) => setCrf(e.currentTarget.value)} />
          </Labeled>
          <Labeled label="编码预设" class="flex-1 min-w-[160px]">
            <input class="input input-bordered w-full" placeholder="fast" value={preset()} onInput={(e) => setPreset(e.currentTarget.value)} />
          </Labeled>
        </div>
      </Show>
      <Labeled label="文件命名规则" hint="占位符：{camera} {camera_id} {year} {month} {day} {hour} {minute} {second} {unix}">
        <input class="input input-bordered w-full" value={pathTemplate()} onInput={(e) => setPath(e.currentTarget.value)} />
      </Labeled>
      <div>
        <button class="btn btn-primary" onClick={() => void save()}>保存</button>
      </div>
    </Card>
  );
}

// ---- Retention ----

function RetentionCard() {
  const [data] = createResource<RetentionPolicy>(() => api("/settings/retention"));
  return <Show when={data()}>{(p) => <RetentionForm initial={p()} />}</Show>;
}

function RetentionForm(props: { initial: RetentionPolicy }) {
  const p = props.initial;
  const [enabled, setEnabled] = createSignal(p.enabled);
  const [maxAgeDays, setAge] = createSignal(String(p.maxAgeDays));
  const [maxTotalSizeGB, setSize] = createSignal(String(p.maxTotalSizeGB));
  const [minFreeSpaceGB, setFree] = createSignal(String(p.minFreeSpaceGB));
  const [deleteAfterUpload, setDel] = createSignal(p.deleteAfterUpload);

  const save = async () => {
    try {
      await api("/settings/retention", {
        method: "PUT",
        body: {
          enabled: enabled(),
          maxAgeDays: parseInt(maxAgeDays(), 10) || 0,
          maxTotalSizeGB: parseFloat(maxTotalSizeGB()) || 0,
          minFreeSpaceGB: parseFloat(minFreeSpaceGB()) || 0,
          deleteAfterUpload: deleteAfterUpload(),
        },
      });
      toast("已保存");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <Card title="存储保留策略（滚动删除）">
      <Check checked={enabled()} onChange={setEnabled} label="启用自动清理" />
      <div class="flex gap-3 flex-wrap">
        <Labeled label="最长保留天数（0=不限）" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" type="number" value={maxAgeDays()} onInput={(e) => setAge(e.currentTarget.value)} />
        </Labeled>
        <Labeled label="最大总容量 GB（0=不限）" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" type="number" step="0.1" value={maxTotalSizeGB()} onInput={(e) => setSize(e.currentTarget.value)} />
        </Labeled>
      </div>
      <Labeled label="最小剩余磁盘空间 GB（0=不限）" class="max-w-[280px]">
        <input class="input input-bordered w-full" type="number" step="0.1" value={minFreeSpaceGB()} onInput={(e) => setFree(e.currentTarget.value)} />
      </Labeled>
      <Check checked={deleteAfterUpload()} onChange={setDel} label="仅删除已上传到 S3 的录像" />
      <div>
        <button class="btn btn-primary" onClick={() => void save()}>保存</button>
      </div>
    </Card>
  );
}

// ---- S3 ----

// ---- Home Assistant ----

function HACard() {
  const [data] = createResource<HAConfig>(() => api("/settings/homeassistant"));
  return <Show when={data()}>{(c) => <HAForm initial={c()} />}</Show>;
}

function HAForm(props: { initial: HAConfig }) {
  const c = props.initial;
  const [enabled, setEnabled] = createSignal(c.enabled);
  const [discoveryPrefix, setPrefix] = createSignal(c.discoveryPrefix || "homeassistant");
  const [baseTopic, setBase] = createSignal(c.baseTopic || "kenko-nvr");

  const save = async () => {
    try {
      await api("/settings/homeassistant", {
        method: "PUT",
        body: { enabled: enabled(), discoveryPrefix: discoveryPrefix(), baseTopic: baseTopic() },
      });
      toast("已保存");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };

  return (
    <Card title="Home Assistant 集成（MQTT 自动发现）">
      <p class="text-sm text-base-content/60">
        通过 MQTT 自动发现把每个摄像头注册为 Home Assistant 设备（移动 binary_sensor + 在线状态）。
        复用「通知告警」中配置的 MQTT 服务器，请先在通知设置中填写 MQTT Broker。
      </p>
      <Check checked={enabled()} onChange={setEnabled} label="启用 Home Assistant 自动发现" />
      <div class="flex gap-3 flex-wrap">
        <Labeled label="发现前缀（discovery prefix）" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="homeassistant" value={discoveryPrefix()} onInput={(e) => setPrefix(e.currentTarget.value)} />
        </Labeled>
        <Labeled label="状态主题前缀（base topic）" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="kenko-nvr" value={baseTopic()} onInput={(e) => setBase(e.currentTarget.value)} />
        </Labeled>
      </div>
      <div>
        <button class="btn btn-primary" onClick={() => void save()}>保存</button>
      </div>
    </Card>
  );
}

// ---- GB28181 (read-only info) ----

function GB28181Card() {
  const [data] = createResource<GB28181Info>(() => api("/gb28181/info"));
  return (
    <Show when={data()}>
      {(info) => (
        <Card title="GB28181 国标接入">
          <Show
            when={info().enabled}
            fallback={
              <p class="text-sm text-base-content/60">
                未启用。在上方「网络服务」中启用 <code>GB28181</code> 并保存即可（无需重启），
                再把设备/NVR 的国标参数指向本服务接入。
              </p>
            }
          >
            <p class="text-sm text-base-content/60">
              GB28181 SIP 服务已启用。请在设备/NVR 的国标参数中填写以下信息，注册成功后在「摄像头管理」中以「GB28181 国标接入」来源添加：
            </p>
            <div class="text-sm space-y-1 mt-1">
              <div>SIP 服务器编号：<code>{info().serverId}</code></div>
              <div>SIP 域：<code>{info().domain}</code></div>
              <div>SIP 服务器地址 / 端口：<code>{info().mediaIp}{info().sipAddr}</code></div>
            </div>
          </Show>
        </Card>
      )}
    </Show>
  );
}

function S3Card() {
  const [data] = createResource<S3Config>(() => api("/settings/s3"));
  return <Show when={data()}>{(c) => <S3Form initial={c()} />}</Show>;
}

function S3Form(props: { initial: S3Config }) {
  const c = props.initial;
  const [enabled, setEnabled] = createSignal(c.enabled);
  const [endpoint, setEndpoint] = createSignal(c.endpoint ?? "");
  const [region, setRegion] = createSignal(c.region ?? "");
  const [bucket, setBucket] = createSignal(c.bucket ?? "");
  const [keyPrefix, setKeyPrefix] = createSignal(c.keyPrefix ?? "");
  const [accessKey, setAccessKey] = createSignal(c.accessKey ?? "");
  const [secretKey, setSecretKey] = createSignal("");
  const [proxyURL, setProxyURL] = createSignal(c.proxyURL ?? "");
  const [useSSL, setUseSSL] = createSignal(c.useSSL);
  const [deleteLocal, setDeleteLocal] = createSignal(c.deleteLocalAfterUpload);

  const collect = (): S3Config => ({
    enabled: enabled(),
    endpoint: endpoint(),
    region: region(),
    bucket: bucket(),
    keyPrefix: keyPrefix(),
    accessKey: accessKey(),
    secretKey: secretKey(),
    proxyURL: proxyURL(),
    useSSL: useSSL(),
    deleteLocalAfterUpload: deleteLocal(),
  });

  const save = async () => {
    try {
      await api("/settings/s3", { method: "PUT", body: collect() });
      toast("已保存");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  };
  const test = async () => {
    toast("正在测试…");
    try {
      await api("/settings/s3/test", { method: "POST", body: collect() });
      toast("连接成功 ✓");
    } catch (e) {
      toast("失败：" + (e as Error).message, "error");
    }
  };

  return (
    <Card title="S3 录像上传">
      <Check checked={enabled()} onChange={setEnabled} label="启用上传" />
      <div class="flex gap-3 flex-wrap">
        <Labeled label="Endpoint" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="s3.amazonaws.com" value={endpoint()} onInput={(e) => setEndpoint(e.currentTarget.value)} />
        </Labeled>
        <Labeled label="Region" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="us-east-1" value={region()} onInput={(e) => setRegion(e.currentTarget.value)} />
        </Labeled>
      </div>
      <div class="flex gap-3 flex-wrap">
        <Labeled label="Bucket" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" value={bucket()} onInput={(e) => setBucket(e.currentTarget.value)} />
        </Labeled>
        <Labeled label="Key 前缀" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" placeholder="nvr/" value={keyPrefix()} onInput={(e) => setKeyPrefix(e.currentTarget.value)} />
        </Labeled>
      </div>
      <div class="flex gap-3 flex-wrap">
        <Labeled label="Access Key" class="flex-1 min-w-[200px]">
          <input class="input input-bordered w-full" value={accessKey()} onInput={(e) => setAccessKey(e.currentTarget.value)} />
        </Labeled>
        <Labeled label="Secret Key" class="flex-1 min-w-[200px]">
          <input type="password" class="input input-bordered w-full" placeholder="（留空表示不修改）" value={secretKey()} onInput={(e) => setSecretKey(e.currentTarget.value)} />
        </Labeled>
      </div>
      <Labeled label="HTTP 代理" hint="可选，例如 http://user:pass@proxy:3128">
        <input class="input input-bordered w-full" placeholder="http://proxy:3128" value={proxyURL()} onInput={(e) => setProxyURL(e.currentTarget.value)} />
      </Labeled>
      <div class="flex gap-6 flex-wrap">
        <Check checked={useSSL()} onChange={setUseSSL} label="使用 HTTPS" />
        <Check checked={deleteLocal()} onChange={setDeleteLocal} label="上传后删除本地文件" />
      </div>
      <div class="flex gap-2.5">
        <button class="btn btn-primary" onClick={() => void save()}>保存</button>
        <button class="btn btn-ghost" onClick={() => void test()}>测试连接</button>
      </div>
    </Card>
  );
}

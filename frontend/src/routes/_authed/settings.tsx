import { createFileRoute } from "@tanstack/solid-router";
import { createResource, createSignal, Show, type JSX } from "solid-js";
import { api } from "~/lib/api";
import { toast } from "~/components/toast";
import type { RecordingConfig, RetentionPolicy, S3Config } from "~/lib/types";

export const Route = createFileRoute("/_authed/settings")({
  component: Settings,
});

function Settings() {
  return (
    <>
      <h1 class="text-[22px] font-semibold mb-5">系统设置</h1>
      <div class="grid gap-4">
        <RecordingCard />
        <RetentionCard />
        <S3Card />
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

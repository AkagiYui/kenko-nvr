// API data shapes, mirroring the Go REST/JSON contract consumed by the UI.

export type SourceType = "rtsp" | "onvif" | "rtmp";
export type CameraState = "running" | "connecting" | "error" | "idle";

export interface TrackInfo {
  kind: string;
  codec: string;
}

export interface CameraStatus {
  id?: string;
  state?: CameraState;
  error?: string;
  live?: boolean;
  recording?: boolean;
  tracks?: TrackInfo[];
}

export interface Camera {
  id: string;
  name: string;
  sourceType: SourceType;
  url?: string;
  username?: string;
  transport?: string;
  record?: boolean;
  enabled?: boolean;
  onvifEnabled?: boolean;
  onvifXAddr?: string;
  onvifUsername?: string;
  onvifProfile?: string;
  status?: CameraStatus;
}

// CameraInput is the create/update payload (includes the write-only passwords).
export interface CameraInput {
  name: string;
  sourceType: SourceType;
  url: string;
  username: string;
  password: string;
  transport: string;
  record: boolean;
  enabled: boolean;
  onvifEnabled: boolean;
  onvifXAddr: string;
  onvifUsername: string;
  onvifPassword: string;
  onvifProfile: string;
}

export interface Recording {
  id: string;
  cameraId: string;
  startTime: string;
  durationMs?: number;
  sizeBytes?: number;
  uploaded?: boolean;
  complete?: boolean;
}

export interface RecordingConfig {
  segmentSeconds: number;
  pathTemplate: string;
  alignToClock: boolean;
  transcode: boolean;
  transcodeVideoCodec: string;
  transcodeCRF: number;
  transcodePreset: string;
}

export interface RetentionPolicy {
  enabled: boolean;
  maxAgeDays: number;
  maxTotalSizeGB: number;
  minFreeSpaceGB: number;
  deleteAfterUpload: boolean;
}

export interface S3Config {
  enabled: boolean;
  endpoint: string;
  region: string;
  bucket: string;
  keyPrefix: string;
  accessKey: string;
  secretKey: string;
  proxyURL: string;
  useSSL: boolean;
  deleteLocalAfterUpload: boolean;
}

export interface OnvifDevice {
  xaddr: string;
}

export interface OnvifProfile {
  token: string;
  streamUri?: string;
}

export interface OnvifProbeResult {
  profiles?: OnvifProfile[];
  info?: { manufacturer?: string; model?: string };
}

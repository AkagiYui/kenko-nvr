// API data shapes, mirroring the Go REST/JSON contract consumed by the UI.

export type SourceType = "rtsp" | "onvif" | "rtmp";
export type CameraState = "running" | "connecting" | "error" | "idle";
export type Role = "admin" | "operator" | "viewer";
export type RecordMode = "continuous" | "motion";

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
  motion?: boolean;
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
  motionEnabled?: boolean;
  recordMode?: RecordMode;
  motionSensitivity?: number;
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
  motionEnabled: boolean;
  recordMode: RecordMode;
  motionSensitivity: number;
}

export interface Recording {
  id: string;
  cameraId: string;
  startTime: string;
  endTime?: string;
  durationMs?: number;
  sizeBytes?: number;
  uploaded?: boolean;
  complete?: boolean;
}

// NvrEvent is a detected event (motion). Named to avoid clashing with DOM Event.
export interface NvrEvent {
  id: string;
  cameraId: string;
  type: string;
  startTime: string;
  endTime?: string;
  score?: number;
}

export interface User {
  id: string;
  username: string;
  role: Role;
  createdAt?: string;
}

export interface Me {
  id: string;
  username: string;
  role: Role;
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

export interface EmailConfig {
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  password: string;
  from: string;
  to: string;
  useTLS: boolean;
}

export interface WebhookConfig {
  enabled: boolean;
  url: string;
}

export interface MQTTConfig {
  enabled: boolean;
  brokerURL: string;
  username: string;
  password: string;
  topic: string;
  clientID: string;
}

export interface WebPushConfig {
  enabled: boolean;
  subject: string;
  publicKey?: string;
}

export interface NotificationConfig {
  enabled: boolean;
  onMotion: boolean;
  onCameraOffline: boolean;
  minIntervalSeconds: number;
  email: EmailConfig;
  webhook: WebhookConfig;
  mqtt: MQTTConfig;
  webPush: WebPushConfig;
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

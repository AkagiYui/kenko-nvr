// API data shapes, mirroring the Go REST/JSON contract consumed by the UI.

export type SourceType = "rtsp" | "onvif" | "rtmp" | "gb28181";
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
  gb28181DeviceId?: string;
  gb28181ChannelId?: string;
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
  gb28181DeviceId: string;
  gb28181ChannelId: string;
}

export interface Recording {
  id: string;
  cameraId: string;
  startTime: number; // epoch milliseconds (UTC); render in the viewer's timezone
  endTime?: number; // epoch milliseconds; null/absent while in progress
  durationMs?: number;
  sizeBytes?: number;
  uploaded?: boolean;
  // localRemoved is true when the local file has been deleted but the clip is
  // preserved on S3; it still plays (streamed back through the NVR) and is shown
  // with a cloud-only indicator.
  localRemoved?: boolean;
  // encrypted is true when the S3 object is client-side encrypted (decrypted
  // transparently on playback/download).
  encrypted?: boolean;
  complete?: boolean;
}

// NvrEvent is a detected event (motion). Named to avoid clashing with DOM Event.
export interface NvrEvent {
  id: string;
  cameraId: string;
  type: string;
  startTime: number; // epoch milliseconds (UTC)
  endTime?: number; // epoch milliseconds; null/absent while in progress
  score?: number;
  // recordings is present only when the event was fetched with
  // ?withRecordings=1: the clips whose span covers the event, oldest first.
  recordings?: Recording[];
}

export interface User {
  id: string;
  username: string;
  role: Role;
  createdAt?: number; // epoch milliseconds (UTC)
}

// --- Face recognition ---

export interface Person {
  id: string;
  name: string;
  notes?: string;
  coverFaceId?: string;
  named?: boolean;
  faceCount: number;
  firstSeen?: number; // epoch ms
  lastSeen?: number; // epoch ms
  createdAt?: number;
  updatedAt?: number;
}

// FaceTrack is one appearance of a person within a recording.
export interface FaceTrack {
  id: string;
  recordingId: string;
  cameraId: string;
  personId: string;
  startTs?: number; // epoch ms
  endTs?: number; // epoch ms
  faceCount: number;
  quality?: number;
  bestFaceId?: string;
  bestOffsetMs?: number; // seek target within the recording
  candSim?: number;
  confirmed?: boolean;
}

export interface PersonDetail extends Person {
  appearances: FaceTrack[];
  recordings?: Recording[];
}

export interface FaceConfig {
  enabled: boolean;
  sidecarURL: string;
  sampleFps: number;
  maxFramesPerJob: number;
  batchSize: number;
  analyzeWidth: number;
  minFaceSize: number;
  detThreshold: number;
  minQuality: number;
  motionGated: boolean;
  realtime: boolean;
  realtimeFps: number;
  matchThreshold: number;
  reviewThreshold: number;
}

export interface FaceStatus {
  enabled: boolean;
  sidecarOk: boolean;
  sidecar?: { status: string; model: string; dim: number; provider: string };
  sidecarErr?: string;
  jobs: Record<string, number>;
  personCount: number;
}

export interface Me {
  id: string;
  username: string;
  role: Role;
  // defaultPassword is true while the account still uses the built-in default
  // password ("admin"), so the UI can prompt the user to change it.
  defaultPassword?: boolean;
}

// SystemStats is the aggregate dashboard footer payload from GET /api/stats.
// Throughput is split from the server's perspective: ingress (下行) is data the
// server receives (camera streams), egress (上行) is data it sends to clients.
export interface SystemStats {
  cameras: number;
  online: number;
  recording: number;
  ingressBytesPerSec: number;
  egressBytesPerSec: number;
  ingressTotalBytes: number;
  egressTotalBytes: number;
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
  // Client-side encryption: recordings are encrypted before upload and decrypted
  // transparently on playback. encryptionKey is write-only (blank means unchanged).
  encryptionEnabled: boolean;
  encryptionKey: string;
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

export type ChannelType = "email" | "webhook" | "mqtt" | "webpush";

// NotificationChannel is one delivery target. Only the config field matching
// `type` is used. `events` empty means "follow the global onMotion/onCameraOffline".
export interface NotificationChannel {
  id: string;
  name: string;
  type: ChannelType;
  enabled: boolean;
  events: string[]; // "motion" | "offline"
  email: EmailConfig;
  webhook: WebhookConfig;
  mqtt: MQTTConfig;
  subject: string; // webpush per-channel VAPID subject
}

export interface NotificationConfig {
  enabled: boolean;
  onMotion: boolean;
  onCameraOffline: boolean;
  minIntervalSeconds: number;
  channels: NotificationChannel[];
  webPush: WebPushConfig; // global VAPID keys (publicKey only on the client)
}

// --- Runtime infrastructure settings (GET/PUT /settings/system) ---

export interface RTMPSettings {
  enabled: boolean;
  addr: string;
}

export interface RTSPSettings {
  transport: string; // "automatic" | "tcp" | "udp"
}

export interface RTSPServerSettings {
  enabled: boolean;
  addr: string;
}

export interface WebRTCSettings {
  enabled: boolean;
  stunServers: string[];
}

export interface GB28181Settings {
  enabled: boolean;
  sipAddr: string;
  serverId: string;
  domain: string;
  password: string;
  mediaIp: string;
  mediaPortMin: number;
  mediaPortMax: number;
}

export interface TranscodeSettings {
  hwaccel: string; // "auto" | "none"/"software" | a specific encoder name
  liveBitrateKbps: number;
  liveGop: number;
}

export interface SystemConfig {
  rtmp: RTMPSettings;
  rtsp: RTSPSettings;
  rtspServer: RTSPServerSettings;
  webrtc: WebRTCSettings;
  gb28181: GB28181Settings;
  transcode: TranscodeSettings;
}

export interface OnvifDevice {
  xaddr: string;
}

export interface OnvifProfile {
  token: string;
  name?: string;
  streamUri?: string;
}

export interface OnvifProbeResult {
  profiles?: OnvifProfile[];
  info?: { manufacturer?: string; model?: string };
}

export interface HAConfig {
  enabled: boolean;
  discoveryPrefix: string;
  baseTopic: string;
}

export interface GB28181Channel {
  id: string;
  name?: string;
  manufacturer?: string;
  model?: string;
  status?: string;
}

export interface GB28181Device {
  id: string;
  name?: string;
  manufacturer?: string;
  model?: string;
  online: boolean;
  lastSeen?: string;
  channels?: GB28181Channel[];
}

export interface GB28181Info {
  enabled: boolean;
  serverId?: string;
  domain?: string;
  sipAddr?: string;
  mediaIp?: string;
}

// VideoWallConfig is the saved video-wall layout: a grid size plus, per size, the
// camera id assigned to each cell (empty string = empty cell).
export interface VideoWallConfig {
  gridSize: number;
  layouts: Record<string, string[]>;
}

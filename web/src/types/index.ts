export type NodeRole = 'controlplane' | 'worker'

export interface NodeOverview {
  ip: string
  hostname: string
  version?: string
  ready: boolean
  role?: NodeRole
  uptime?: string
  cpuUsage?: number // percentage e.g. 15
  memoryUsage?: string // e.g. "1.8 / 4.0 GB"
  kubernetesVersion?: string
  servicesSummary?: {
    etcd?: 'Healthy' | 'Degraded' | 'N/A'
    kubelet?: 'Healthy' | 'Degraded'
    containerd?: 'Healthy' | 'Degraded'
    apid?: 'Healthy' | 'Degraded'
  }
}

export interface ClusterInfo {
  name: string
  healthy: boolean
  talosVersion: string
  kubernetesVersion: string
  endpoint: string
  totalNodes: number
  readyNodes: number
  controlPlaneCount: number
  workerCount: number
}

export type ServiceState = 'Running' | 'Waiting' | 'Preparing' | 'Degraded' | 'Stopped'

export interface TalosService {
  id: string
  name: string
  state: ServiceState
  healthy: boolean
  description: string
  uptime?: string
  restarts?: number
}

export interface DmesgLogLine {
  id: string
  timestamp: string
  raw: string
  prefix?: string
  level?: 'info' | 'warn' | 'error' | 'kern' | 'debug'
}

export type TabKey = 'nodes' | 'storage' | 'config' | 'workloads' | 'operations'

// Storage & Disks interfaces
export interface DiskPartition {
  device: string // e.g. "/dev/sda1"
  size: string // e.g. "512 MB"
  type?: string // e.g. "EFI System", "Talos State"
  filesystem?: string // e.g. "vfat", "xfs", "ext4"
  mountpoint?: string // e.g. "/boot/efi", "/var", "/system/state"
  label?: string
  used?: string // e.g. "64 MB"
  usedPercent?: number // e.g. 12
}

export interface PhysicalDisk {
  name: string // e.g. "/dev/sda"
  model?: string // e.g. "VirtIO SCSI Disk"
  serial?: string
  size: string // e.g. "50.0 GB"
  bus: string // e.g. "SCSI", "NVMe", "VirtIO", "SATA"
  type: 'SSD' | 'HDD' | 'NVMe' | 'Virtual'
  healthy: boolean
  temp?: string
  readOnly?: boolean
  partitions: DiskPartition[]
}

export interface NodeDisksOverview {
  nodeIP: string
  hostname: string
  disks: PhysicalDisk[]
  totalStorage: string
  usedStorage: string
  usedPercent: number
}

// MachineConfig interfaces
export interface MachineConfigData {
  nodeIP: string
  hostname: string
  version: string
  role: NodeRole
  configYaml: string
  fetchedAt: string
}

// Kubernetes Workloads / Pods interfaces
export type PodStatus = 'Running' | 'Pending' | 'Succeeded' | 'Failed' | 'CrashLoopBackOff'

export interface K8sPod {
  id: string
  name: string
  namespace: string
  nodeName: string
  node?: string
  nodeIP?: string
  status: PodStatus
  readyContainers: string // e.g. "1/1"
  readyCount?: number
  restarts: number
  ip: string
  podIp?: string
  age: string
  cpu?: string
  memory?: string
}

// Operations & etcd interfaces
export interface EtcdMember {
  id: string
  name: string
  peerURLs: string[]
  clientURLs: string[]
  leader: boolean
  dbSize: string
  healthy: boolean
  errors?: string[]
}

export interface EtcdClusterHealth {
  healthy: boolean
  members: EtcdMember[]
  leaderId: string
  leaderName: string
  alarms: string[]
  totalDbSize: string
  raftTerm: number
  raftIndex: number
}

export interface BootstrapCheckItem {
  id: string
  title: string
  description: string
  status: 'pending' | 'success' | 'warning' | 'error'
  detail?: string
}

// Proxmox VE Integration interfaces
export interface ProxmoxMemoryStatus {
  total: number
  used: number
  free: number
  available: number
  usagePercent: number
}

export interface ProxmoxStorageStatus {
  name: string
  total: number
  used: number
  free: number
  usagePercent: number
  type: string
}

export interface ProxmoxNodeStatus {
  node: string
  uptime: number
  cpu: number
  cpuUsagePercent: number
  cpuCores: number
  cpuModel: string
  memory: ProxmoxMemoryStatus
  storage: ProxmoxStorageStatus
}

export interface ProxmoxStatusResponse {
  configured: boolean
  message?: string
  error?: string
  node?: string
  status?: ProxmoxNodeStatus
}

export interface CreateWorkerParams {
  vmid?: number
  name?: string
  cores?: number
  memoryMB?: number
  diskGB?: number
  storage?: string
  iso?: string
  bridge?: string
  macAddr?: string
  start?: boolean
}

export interface CreateWorkerResult {
  vmid: number
  name: string
  taskId?: string
  status: string
  message: string
}

export interface DeleteWorkerResult {
  success: boolean
  vmid: number
  message: string
}

// Telegram Alerting interfaces
export interface AlertRecord {
  id: string
  level: 'CRITICAL' | 'WARNING' | 'RECOVERED' | 'INFO'
  title: string
  message: string
  timestamp: string
  success: boolean
  error?: string
}

export interface AlertsConfig {
  enabled: boolean
  bot_configured: boolean
  bot_token?: string
  bot_token_masked?: string
  botToken?: string
  chat_id?: string
  chat_id_masked?: string
  chatID?: string
  min_level?: string
  minLevel?: string
  check_interval_seconds?: number
  watcher_running?: boolean
  monitored_nodes?: number
  active_alerts_count?: number
  activeAlertsCount?: number
  last_check_time?: string | null
  lastCheckTime?: string | null
  recent_alerts?: AlertRecord[]
  recentAlerts?: AlertRecord[]
}

export interface UpdateAlertsPayload {
  bot_token?: string
  chat_id?: string
  enabled?: boolean
  min_level?: string
}

// Authentication & Audit interfaces
export interface UserInfo {
  username: string
  role: 'admin' | 'viewer'
}

export interface AuthResponse {
  token: string
  user: UserInfo
  expiresIn?: number
}

export interface MeResponse {
  authenticated: boolean
  user: UserInfo
}

export interface AuditLogEvent {
  id: string
  timestamp: string
  action: string
  user: string
  ip: string
  status: 'success' | 'failed'
  details?: Record<string, any>
}

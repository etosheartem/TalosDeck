import { ref } from 'vue'
import type {
  ClusterInfo,
  NodeOverview,
  TalosService,
  PhysicalDisk,
  NodeDisksOverview,
  MachineConfigData,
  K8sPod,
  EtcdClusterHealth,
  BootstrapCheckItem,
  ProxmoxStatusResponse,
  CreateWorkerParams,
  CreateWorkerResult,
  DeleteWorkerResult,
  AlertsConfig,
  UpdateAlertsPayload,
  UserInfo,
  AuthResponse,
  MeResponse,
  AuditLogEvent,
} from '../types'
import { formatBytes, sizeToGiB } from '../utils/storage'

export type { AuthResponse, MeResponse, UserInfo, AuditLogEvent }

const fetchWithTimeout = async (
  input: RequestInfo | URL,
  init: RequestInit = {},
  timeoutMs = 15000,
): Promise<Response> => {
  const controller = new AbortController()
  const timeoutId = setTimeout(() => controller.abort(), timeoutMs)

  try {
    return await fetch(input, { ...init, signal: controller.signal })
  } finally {
    clearTimeout(timeoutId)
  }
}

const responseError = async (res: Response, fallback: string): Promise<Error> => {
  if (res.status === 401) {
    localStorage.removeItem('talosdeck_token')
    isAuthenticated.value = false
    currentUser.value = { username: 'guest', role: 'viewer' }
  }
  const data = await res.json().catch(() => ({}))
  const error = new Error(data.error || `${fallback} (HTTP ${res.status})`)
  if (res.status === 401) error.name = 'UnauthorizedError'
  return error
}

const MOCK_NODES: NodeOverview[] = [
  {
    ip: '10.42.0.110',
    hostname: 'talos-cp-1',
    version: 'v1.14.0',
    ready: true,
    role: 'controlplane',
    uptime: '14 days, 6 hours',
    cpuUsage: 14,
    memoryUsage: '2.1 / 8.0 GB',
    kubernetesVersion: 'v1.32.2',
    servicesSummary: {
      etcd: 'Healthy',
      kubelet: 'Healthy',
      containerd: 'Healthy',
      apid: 'Healthy',
    },
  },
  {
    ip: '10.42.0.111',
    hostname: 'talos-worker-1',
    version: 'v1.14.0',
    ready: true,
    role: 'worker',
    uptime: '14 days, 5 hours',
    cpuUsage: 28,
    memoryUsage: '3.4 / 16.0 GB',
    kubernetesVersion: 'v1.32.2',
    servicesSummary: {
      etcd: 'N/A',
      kubelet: 'Healthy',
      containerd: 'Healthy',
      apid: 'Healthy',
    },
  },
  {
    ip: '10.42.0.112',
    hostname: 'talos-worker-2',
    version: 'v1.14.0',
    ready: true,
    role: 'worker',
    uptime: '14 days, 5 hours',
    cpuUsage: 8,
    memoryUsage: '1.9 / 16.0 GB',
    kubernetesVersion: 'v1.32.2',
    servicesSummary: {
      etcd: 'N/A',
      kubelet: 'Healthy',
      containerd: 'Healthy',
      apid: 'Healthy',
    },
  },
]

export const fetchClusterInfo = async (nodes: NodeOverview[]): Promise<ClusterInfo> => {
  try {
    const res = await fetchWithTimeout('/api/cluster')
    if (res.ok) return await res.json()
  } catch (err) {
    console.warn('Endpoint /api/cluster not reachable, deriving cluster info from nodes:', err)
  }

  const readyCount = nodes.filter((node) => node.ready).length
  const controlPlaneCount = nodes.filter((node) => node.role === 'controlplane').length

  return {
    name: 'lab-k8s',
    healthy: readyCount === nodes.length && nodes.length > 0,
    talosVersion: nodes[0]?.version || 'v1.14.0',
    kubernetesVersion: nodes[0]?.kubernetesVersion || 'v1.32.2',
    endpoint: 'https://10.42.0.110:6443',
    totalNodes: nodes.length,
    readyNodes: readyCount,
    controlPlaneCount,
    workerCount: nodes.filter((node) => node.role === 'worker').length,
  }
}

export const fetchNodes = async (): Promise<{ nodes: NodeOverview[]; isMock: boolean }> => {
  try {
    const res = await fetchWithTimeout('/api/nodes')

    if (!res.ok) {
      throw new Error(`HTTP error ${res.status}`)
    }

    const data = await res.json()
    if (!Array.isArray(data)) {
      throw new Error('Invalid /api/nodes response: expected an array')
    }

    const parsed: NodeOverview[] = data.map((item: any, idx: number) => {
      const hostname = item.hostname || `talos-node-${idx + 1}`
      const isCP = hostname.includes('cp') || hostname.includes('master') || idx === 0
      return {
        ip: item.ip || '10.42.0.110',
        hostname,
        version: item.version || 'v1.14.0',
        ready: item.ready !== undefined ? Boolean(item.ready) : true,
        role: item.role || (isCP ? 'controlplane' : 'worker'),
        uptime: item.uptime || '14 days',
        cpuUsage: item.cpuUsage ?? 0,
        memoryUsage: item.memoryUsage || (isCP ? '2.1 / 8.0 GB' : '3.4 / 16.0 GB'),
        kubernetesVersion: item.kubernetesVersion || 'v1.32.2',
        servicesSummary: item.servicesSummary || {
          etcd: isCP ? 'Healthy' : 'N/A',
          kubelet: 'Healthy',
          containerd: 'Healthy',
          apid: 'Healthy',
        },
      }
    })

    return { nodes: parsed, isMock: false }
  } catch (err) {
    console.warn('Backend /api/nodes not reachable, using fallback mock data:', err)
  }

  return { nodes: MOCK_NODES, isMock: true }
}

export const fetchNodeServices = async (ip: string, isControlPlane: boolean): Promise<TalosService[]> => {
  try {
    const res = await fetchWithTimeout(`/api/nodes/${ip}/services`)

    if (res.ok) {
      const data = await res.json()
      if (Array.isArray(data)) {
        return data
      }
    }
  } catch {
    // Graceful fallback to mock Talos services
  }

  const baseServices: TalosService[] = [
    {
      id: 'apid',
      name: 'apid',
      state: 'Running',
      healthy: true,
      description: 'Talos OS API Daemon (gRPC :50000 mTLS)',
      uptime: '14d 6h',
      restarts: 0,
    },
    {
      id: 'containerd',
      name: 'containerd',
      state: 'Running',
      healthy: true,
      description: 'Container Runtime Engine (CRI)',
      uptime: '14d 6h',
      restarts: 0,
    },
    {
      id: 'kubelet',
      name: 'kubelet',
      state: 'Running',
      healthy: true,
      description: 'Kubernetes Node Agent',
      uptime: '14d 6h',
      restarts: 0,
    },
    {
      id: 'machined',
      name: 'machined',
      state: 'Running',
      healthy: true,
      description: 'Talos Machine Controller Manager',
      uptime: '14d 6h',
      restarts: 0,
    },
    {
      id: 'networkd',
      name: 'networkd',
      state: 'Running',
      healthy: true,
      description: 'Network link and route configuration',
      uptime: '14d 6h',
      restarts: 0,
    },
    {
      id: 'timed',
      name: 'timed',
      state: 'Running',
      healthy: true,
      description: 'NTP System Clock Synchronization',
      uptime: '14d 6h',
      restarts: 0,
    },
    {
      id: 'udevd',
      name: 'udevd',
      state: 'Running',
      healthy: true,
      description: 'Hardware and kernel device event manager',
      uptime: '14d 6h',
      restarts: 0,
    },
  ]

  if (isControlPlane) {
    baseServices.unshift({
      id: 'etcd',
      name: 'etcd',
      state: 'Running',
      healthy: true,
      description: 'Distributed reliable key-value store for Kubernetes',
      uptime: '14d 6h',
      restarts: 0,
    })
  }

  return baseServices
}

export const rebootNode = async (ip: string): Promise<{ success: boolean; message?: string }> => {
  try {
    const res = await fetchWithTimeout(`/api/nodes/${ip}/reboot`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    }, 15000)
    if (!res.ok) {
	  throw await responseError(res, `Failed to reboot node ${ip}`)
    }
    return { success: true }
  } catch (err: any) {
    console.warn(`Reboot API error for ${ip}:`, err)
	throw err
  }
}

export const getDmesgWsUrl = (ip: string): string => {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/ws/nodes/${encodeURIComponent(ip)}/dmesg`
}

// ----------------------------------------------------
// 1. Storage & Disks API (/api/nodes/:ip/disks)
// ----------------------------------------------------
export const MOCK_DISKS_MAP: Record<string, PhysicalDisk[]> = {
  '10.42.0.110': [
    {
      name: '/dev/sda',
      model: 'VirtIO SCSI OS Disk',
      serial: 'QM00001-BOOT',
      size: '50.0 GB',
      bus: 'SCSI',
      type: 'SSD',
      healthy: true,
      temp: '33°C',
      partitions: [
        {
          device: '/dev/sda1',
          size: '512 MB',
          type: 'EFI System Partition',
          filesystem: 'vfat',
          mountpoint: '/boot/efi',
          label: 'EFI',
          used: '64 MB',
          usedPercent: 12,
        },
        {
          device: '/dev/sda2',
          size: '10 MB',
          type: 'Talos BIOS Boot',
          filesystem: 'none',
          mountpoint: '-',
          label: 'BIOS',
          used: '0 MB',
          usedPercent: 0,
        },
        {
          device: '/dev/sda3',
          size: '2.0 GB',
          type: 'Talos State (SYSTEM)',
          filesystem: 'xfs',
          mountpoint: '/system/state',
          label: 'STATE',
          used: '410 MB',
          usedPercent: 20,
        },
        {
          device: '/dev/sda4',
          size: '47.5 GB',
          type: 'Talos Ephemeral Data',
          filesystem: 'xfs',
          mountpoint: '/var',
          label: 'EPHEMERAL',
          used: '8.4 GB',
          usedPercent: 18,
        },
      ],
    },
  ],
  '10.42.0.111': [
    {
      name: '/dev/sda',
      model: 'VirtIO SCSI OS Disk',
      serial: 'QM00002-BOOT',
      size: '80.0 GB',
      bus: 'SCSI',
      type: 'SSD',
      healthy: true,
      temp: '35°C',
      partitions: [
        {
          device: '/dev/sda1',
          size: '512 MB',
          type: 'EFI System Partition',
          filesystem: 'vfat',
          mountpoint: '/boot/efi',
          label: 'EFI',
          used: '64 MB',
          usedPercent: 12,
        },
        {
          device: '/dev/sda2',
          size: '10 MB',
          type: 'Talos BIOS Boot',
          filesystem: 'none',
          mountpoint: '-',
          label: 'BIOS',
          used: '0 MB',
          usedPercent: 0,
        },
        {
          device: '/dev/sda3',
          size: '2.0 GB',
          type: 'Talos State (SYSTEM)',
          filesystem: 'xfs',
          mountpoint: '/system/state',
          label: 'STATE',
          used: '390 MB',
          usedPercent: 19,
        },
        {
          device: '/dev/sda4',
          size: '77.5 GB',
          type: 'Talos Ephemeral Data',
          filesystem: 'xfs',
          mountpoint: '/var',
          label: 'EPHEMERAL',
          used: '19.2 GB',
          usedPercent: 25,
        },
      ],
    },
    {
      name: '/dev/sdb',
      model: 'NVMe HighSpeed PV Disk',
      serial: 'NVME-0001-VOL',
      size: '200.0 GB',
      bus: 'VirtIO',
      type: 'NVMe',
      healthy: true,
      temp: '38°C',
      partitions: [
        {
          device: '/dev/sdb1',
          size: '200.0 GB',
          type: 'OpenEBS LocalPV CSI Storage',
          filesystem: 'ext4',
          mountpoint: '/var/mnt/storage',
          label: 'LOCAL-PV',
          used: '36.5 GB',
          usedPercent: 18,
        },
      ],
    },
  ],
  '10.42.0.112': [
    {
      name: '/dev/sda',
      model: 'VirtIO SCSI OS Disk',
      serial: 'QM00003-BOOT',
      size: '80.0 GB',
      bus: 'SCSI',
      type: 'SSD',
      healthy: true,
      temp: '34°C',
      partitions: [
        {
          device: '/dev/sda1',
          size: '512 MB',
          type: 'EFI System Partition',
          filesystem: 'vfat',
          mountpoint: '/boot/efi',
          label: 'EFI',
          used: '64 MB',
          usedPercent: 12,
        },
        {
          device: '/dev/sda2',
          size: '10 MB',
          type: 'Talos BIOS Boot',
          filesystem: 'none',
          mountpoint: '-',
          label: 'BIOS',
          used: '0 MB',
          usedPercent: 0,
        },
        {
          device: '/dev/sda3',
          size: '2.0 GB',
          type: 'Talos State (SYSTEM)',
          filesystem: 'xfs',
          mountpoint: '/system/state',
          label: 'STATE',
          used: '380 MB',
          usedPercent: 19,
        },
        {
          device: '/dev/sda4',
          size: '77.5 GB',
          type: 'Talos Ephemeral Data',
          filesystem: 'xfs',
          mountpoint: '/var',
          label: 'EPHEMERAL',
          used: '12.8 GB',
          usedPercent: 16,
        },
      ],
    },
    {
      name: '/dev/sdb',
      model: 'NVMe HighSpeed PV Disk',
      serial: 'NVME-0002-VOL',
      size: '200.0 GB',
      bus: 'VirtIO',
      type: 'NVMe',
      healthy: true,
      temp: '37°C',
      partitions: [
        {
          device: '/dev/sdb1',
          size: '200.0 GB',
          type: 'OpenEBS LocalPV CSI Storage',
          filesystem: 'ext4',
          mountpoint: '/var/mnt/storage',
          label: 'LOCAL-PV',
          used: '19.4 GB',
          usedPercent: 10,
        },
      ],
    },
  ],
}

const normalizeDiskType = (value: unknown): PhysicalDisk['type'] => {
  const diskType = String(value || '').toLowerCase()
  if (diskType.includes('nvme')) return 'NVMe'
  if (diskType.includes('ssd')) return 'SSD'
  if (diskType.includes('hdd')) return 'HDD'
  return 'Virtual'
}

const normalizePhysicalDisk = (raw: any): PhysicalDisk => {
  const rawSize = raw.sizeBytes ?? raw.size
  const sizeBytes = typeof rawSize === 'number' && Number.isFinite(rawSize) ? rawSize : undefined
  const partitions = Array.isArray(raw.partitions) ? raw.partitions : []

  return {
    name: raw.name || raw.devicePath || raw.deviceName || 'unknown',
    model: raw.model,
    serial: raw.serial,
    size: raw.prettySize || (sizeBytes !== undefined ? formatBytes(sizeBytes) : String(raw.size || '0 B')),
    sizeBytes,
    bus: raw.bus || 'Unknown',
    type: normalizeDiskType(raw.type),
    healthy: raw.healthy ?? true,
    temp: raw.temp,
    readOnly: raw.readOnly ?? raw.readonly ?? false,
    partitions: partitions.map((partition: any) => {
      const rawPartitionSize = partition.sizeBytes ?? partition.size
      const partitionSizeBytes =
        typeof rawPartitionSize === 'number' && Number.isFinite(rawPartitionSize)
          ? rawPartitionSize
          : undefined

      return {
        device: partition.device || partition.location || partition.id || 'unknown',
        size:
          partition.prettySize ||
          (partitionSizeBytes !== undefined
            ? formatBytes(partitionSizeBytes)
            : String(partition.size || '0 B')),
        sizeBytes: partitionSizeBytes,
        type: partition.type,
        filesystem: partition.filesystem,
        mountpoint: partition.mountpoint || partition.mountPath,
        label: partition.label,
        used: partition.used,
        usedPercent: partition.usedPercent,
      }
    }),
  }
}

export const fetchNodeDisks = async (nodeIP: string): Promise<PhysicalDisk[]> => {
  try {
    const res = await fetchWithTimeout(`/api/nodes/${nodeIP}/disks`)
    if (res.ok) {
      const data = await res.json()
      if (Array.isArray(data) && data.length > 0) {
        return data.map(normalizePhysicalDisk)
      }
    }
  } catch (err) {
    console.warn(`Endpoint /api/nodes/${nodeIP}/disks not available, using fallback:`, err)
  }

  return MOCK_DISKS_MAP[nodeIP] || MOCK_DISKS_MAP['10.42.0.110']
}

export const fetchAllNodeDisks = async (nodes: NodeOverview[]): Promise<NodeDisksOverview[]> => {
  const list: NodeDisksOverview[] = []
  for (const node of nodes) {
    const disks = await fetchNodeDisks(node.ip)
    // Approximate total and used
    let totalGB = 0
    let usedGB = 0
    disks.forEach((d) => {
      totalGB += sizeToGiB(d.sizeBytes ?? d.size)
      d.partitions.forEach((p) => {
        if (p.used) {
          usedGB += sizeToGiB(p.used)
        }
      })
    })

    const usedPercent = totalGB > 0 ? Math.round((usedGB / totalGB) * 100) : 0

    list.push({
      nodeIP: node.ip,
      hostname: node.hostname,
      disks,
      totalStorage: `${totalGB.toFixed(1)} GB`,
      usedStorage: `${usedGB.toFixed(1)} GB`,
      usedPercent,
    })
  }
  return list
}

// ----------------------------------------------------
// 2. MachineConfig API (/api/nodes/:ip/config)
// ----------------------------------------------------
const generateMockConfigYaml = (ip: string, hostname: string, isCP: boolean): string => {
  const roleType = isCP ? 'controlplane' : 'worker'
  return `version: v1alpha1
debug: false
persist: true
machine:
  type: ${roleType}
  token: 9x8f0a.12ab34cd56ef7890
  ca:
    crt: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCg==
  features:
    rbac: true
    stableHostname: true
    apidCheckExtKeyUsage: true
    diskQuotaSupport: true
  network:
    hostname: ${hostname}
    interfaces:
      - interface: eth0
        dhcp: false
        addresses:
          - ${ip}/24
        routes:
          - network: 0.0.0.0/0
            gateway: 10.42.0.1
    nameservers:
      - 1.1.1.1
      - 8.8.8.8
  install:
    disk: /dev/sda
    image: ghcr.io/siderolabs/installer:v1.14.0
    bootloader: true
    wipe: false
  kubelet:
    image: ghcr.io/siderolabs/kubelet:v1.32.2
    extraArgs:
      rotate-server-certificates: "true"
      node-labels: "topology.kubernetes.io/zone=dc1,talos.deck/managed=true"
  sysctls:
    vm.max_map_count: "262144"
    fs.inotify.max_user_watches: "1048576"
    fs.inotify.max_user_instances: "8192"
  time:
    servers:
      - time.cloudflare.com
      - 0.pool.ntp.org
cluster:
  controlPlane:
    endpoint: https://10.42.0.110:6443
  clusterName: lab-k8s
  network:
    cni:
      name: flannel
    podSubnets:
      - 10.244.0.0/16
    serviceSubnets:
      - 10.96.0.0/12
  token: 8b7c6d.0987654321fedcba
  ca:
    crt: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCg==
${
  isCP
    ? `  etcd:
    ca:
      crt: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCg==
  apiServer:
    image: registry.k8s.io/kube-apiserver:v1.32.2
    certSANs:
      - ${ip}
      - 127.0.0.1
      - cluster.local
    extraArgs:
      anonymous-auth: "true"`
    : `  # Worker node connected to API controlPlane endpoint`
}
`
}

export const fetchNodeConfig = async (
  nodeIP: string,
  hostname = 'talos-node',
  isCP = false,
): Promise<MachineConfigData> => {
  try {
    const res = await fetchWithTimeout(`/api/nodes/${nodeIP}/config`, {
      headers: { ...getAuthHeaders() },
    })
    if (res.ok) {
      const data = await res.json()
      if (data && typeof data.configYaml === 'string') {
        return {
          nodeIP,
          hostname: data.hostname || hostname,
          version: data.version || 'v1.14.0',
          role: isCP ? 'controlplane' : 'worker',
          configYaml: data.configYaml,
          fetchedAt: new Date().toLocaleTimeString(),
        }
      }
    }
	if (res.status === 401) throw await responseError(res, 'Authentication required to read MachineConfig')
  } catch (err) {
	if (err instanceof Error && err.name === 'UnauthorizedError') throw err
    console.warn(`Endpoint /api/nodes/${nodeIP}/config not reachable, using fallback:`, err)
  }

  return {
    nodeIP,
    hostname,
    version: 'v1.14.0',
    role: isCP ? 'controlplane' : 'worker',
    configYaml: generateMockConfigYaml(nodeIP, hostname, isCP),
    fetchedAt: new Date().toLocaleTimeString(),
  }
}

// ----------------------------------------------------
// 3. Operations & etcd API (/api/cluster/etcd)
// ----------------------------------------------------
export const MOCK_ETCD_HEALTH: EtcdClusterHealth = {
  healthy: true,
  leaderId: '8e9e35c7d0f6fa98',
  leaderName: 'talos-cp-1',
  totalDbSize: '24.8 MB',
  raftTerm: 4,
  raftIndex: 384920,
  alarms: [],
  members: [
    {
      id: '8e9e35c7d0f6fa98',
      name: 'talos-cp-1',
      peerURLs: ['https://10.42.0.110:2380'],
      clientURLs: ['https://10.42.0.110:2379'],
      leader: true,
      dbSize: '24.8 MB',
      healthy: true,
    },
  ],
}

export const fetchEtcdHealth = async (): Promise<EtcdClusterHealth> => {
  try {
    const res = await fetchWithTimeout('/api/cluster/etcd')
    if (res.ok) {
      const data = await res.json()
      if (data && data.members) {
        return {
          ...data,
          members: data.members.map((member: any) => ({
            ...member,
            name: member.name || member.hostname || '',
            peerURLs: member.peerURLs || member.peerUrls || [],
            clientURLs: member.clientURLs || member.clientUrls || [],
            leader: Boolean(member.leader),
            dbSize: member.dbSize || '',
          })),
        }
      }
    }
  } catch (err) {
    console.warn('Endpoint /api/cluster/etcd not reachable, using fallback:', err)
  }

  return MOCK_ETCD_HEALTH
}

// ----------------------------------------------------
// 4. Workloads / K8s Pods API (/api/k8s/pods)
// ----------------------------------------------------
export const MOCK_PODS: K8sPod[] = [
  // kube-system
  {
    id: 'pod-1',
    name: 'kube-apiserver-talos-cp-1',
    namespace: 'kube-system',
    nodeName: 'talos-cp-1',
    nodeIP: '10.42.0.110',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.42.0.110',
    age: '14d',
    cpu: '45m',
    memory: '310Mi',
  },
  {
    id: 'pod-2',
    name: 'kube-controller-manager-talos-cp-1',
    namespace: 'kube-system',
    nodeName: 'talos-cp-1',
    nodeIP: '10.42.0.110',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.42.0.110',
    age: '14d',
    cpu: '28m',
    memory: '142Mi',
  },
  {
    id: 'pod-3',
    name: 'kube-scheduler-talos-cp-1',
    namespace: 'kube-system',
    nodeName: 'talos-cp-1',
    nodeIP: '10.42.0.110',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.42.0.110',
    age: '14d',
    cpu: '12m',
    memory: '56Mi',
  },
  {
    id: 'pod-4',
    name: 'kube-flannel-ds-m4kq2',
    namespace: 'kube-system',
    nodeName: 'talos-cp-1',
    nodeIP: '10.42.0.110',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.42.0.110',
    age: '14d',
    cpu: '8m',
    memory: '38Mi',
  },
  {
    id: 'pod-5',
    name: 'kube-flannel-ds-9k8lp',
    namespace: 'kube-system',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.42.0.111',
    age: '14d',
    cpu: '9m',
    memory: '39Mi',
  },
  {
    id: 'pod-6',
    name: 'kube-flannel-ds-v7x2z',
    namespace: 'kube-system',
    nodeName: 'talos-worker-2',
    nodeIP: '10.42.0.112',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.42.0.112',
    age: '14d',
    cpu: '7m',
    memory: '37Mi',
  },
  {
    id: 'pod-7',
    name: 'coredns-668d6bf9bc-h8pt4',
    namespace: 'kube-system',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.1.2',
    age: '14d',
    cpu: '5m',
    memory: '22Mi',
  },
  {
    id: 'pod-8',
    name: 'coredns-668d6bf9bc-s9r2k',
    namespace: 'kube-system',
    nodeName: 'talos-worker-2',
    nodeIP: '10.42.0.112',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.2.2',
    age: '14d',
    cpu: '4m',
    memory: '21Mi',
  },
  // ingress-nginx
  {
    id: 'pod-9',
    name: 'ingress-nginx-controller-7f4b89d4f6-8km2v',
    namespace: 'ingress-nginx',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.1.5',
    age: '12d',
    cpu: '35m',
    memory: '190Mi',
  },
  {
    id: 'pod-10',
    name: 'ingress-nginx-controller-7f4b89d4f6-n7x5q',
    namespace: 'ingress-nginx',
    nodeName: 'talos-worker-2',
    nodeIP: '10.42.0.112',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.2.6',
    age: '12d',
    cpu: '31m',
    memory: '185Mi',
  },
  // cert-manager
  {
    id: 'pod-11',
    name: 'cert-manager-57d47bb85c-2g6ql',
    namespace: 'cert-manager',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.1.7',
    age: '10d',
    cpu: '11m',
    memory: '48Mi',
  },
  {
    id: 'pod-12',
    name: 'cert-manager-cainjector-7d8b584d4b-mfd2j',
    namespace: 'cert-manager',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.1.8',
    age: '10d',
    cpu: '14m',
    memory: '62Mi',
  },
  {
    id: 'pod-13',
    name: 'cert-manager-webhook-654854f8-p98tr',
    namespace: 'cert-manager',
    nodeName: 'talos-worker-2',
    nodeIP: '10.42.0.112',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.2.9',
    age: '10d',
    cpu: '8m',
    memory: '34Mi',
  },
  // monitoring
  {
    id: 'pod-14',
    name: 'prometheus-k8s-0',
    namespace: 'monitoring',
    nodeName: 'talos-worker-2',
    nodeIP: '10.42.0.112',
    status: 'Running',
    readyContainers: '2/2',
    restarts: 1,
    ip: '10.244.2.14',
    age: '8d',
    cpu: '120m',
    memory: '680Mi',
  },
  {
    id: 'pod-15',
    name: 'grafana-765d77494f-4d9pt',
    namespace: 'monitoring',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.1.18',
    age: '8d',
    cpu: '24m',
    memory: '110Mi',
  },
  // default
  {
    id: 'pod-16',
    name: 'talosdeck-web-74b8898b79-5xvt2',
    namespace: 'default',
    nodeName: 'talos-worker-1',
    nodeIP: '10.42.0.111',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.1.25',
    age: '4d',
    cpu: '18m',
    memory: '84Mi',
  },
  {
    id: 'pod-17',
    name: 'talosdeck-web-74b8898b79-kl9m8',
    namespace: 'default',
    nodeName: 'talos-worker-2',
    nodeIP: '10.42.0.112',
    status: 'Running',
    readyContainers: '1/1',
    restarts: 0,
    ip: '10.244.2.31',
    age: '4d',
    cpu: '15m',
    memory: '79Mi',
  },
]

export const fetchK8sPods = async (): Promise<K8sPod[]> => {
  try {
    const res = await fetchWithTimeout('/api/k8s/pods')
    if (res.ok) {
      const data = await res.json()
      if (Array.isArray(data)) {
        return data.map((p: any) => ({
          ...p,
          nodeName: p.nodeName || p.node || '',
          ip: p.ip || p.podIp || '',
          readyContainers: String(p.readyContainers || (p.readyCount != null ? `${p.readyCount}/${p.totalContainers || 1}` : '0/0')),
        }))
      }
    }
  } catch (err) {
    console.warn('Endpoint /api/k8s/pods not reachable, using fallback:', err)
  }

  return MOCK_PODS
}

export const fetchEtcdStatus = fetchEtcdHealth

export const fetchPods = async (): Promise<{ pods: K8sPod[] }> => {
  const pods = await fetchK8sPods()
  return { pods }
}

// ----------------------------------------------------
// 5. Operations & Bootstrap Diagnostics
// ----------------------------------------------------
export const runBootstrapCheck = async (): Promise<BootstrapCheckItem[]> => {
  // Simulate diagnosis with small delay
  await new Promise((r) => setTimeout(r, 600))
  return [
    {
      id: 'talos-api',
      title: 'Talos OS API & mTLS',
      description: 'TCP :50000 mTLS connectivity to all cluster nodes',
      status: 'success',
      detail: 'Latency < 2ms across all 3 nodes (10.42.0.110, 10.42.0.111, 10.42.0.112)',
    },
    {
      id: 'etcd-quorum',
      title: 'etcd Cluster Quorum',
      description: 'Distributed key-value store consensus and raft status',
      status: 'success',
      detail: 'Leader: talos-cp-1 (ID 8e9e35c7d0f6fa98), Raft Term 4, 0 alarms',
    },
    {
      id: 'k8s-apiserver',
      title: 'Kubernetes Control Plane',
      description: 'kube-apiserver /readyz and /livez endpoints',
      status: 'success',
      detail: 'HTTP 200 OK via https://10.42.0.110:6443',
    },
    {
      id: 'cni-network',
      title: 'CNI Fabric & PodCIDR',
      description: 'Flannel overlay network and node routing table',
      status: 'success',
      detail: 'Subnet 10.244.0.0/16 distributed across 3 nodes',
    },
    {
      id: 'coredns-service',
      title: 'CoreDNS Resolution',
      description: 'In-cluster DNS service responsiveness',
      status: 'success',
      detail: '2/2 replicas active, resolved kubernetes.default.svc.cluster.local in 1.4ms',
    },
  ]
}

export const toggleMaintenanceMode = async (
  nodeIP: string,
  enable: boolean,
): Promise<{ success: boolean; message: string }> => {
  try {
    const res = await fetchWithTimeout(`/api/nodes/${nodeIP}/maintenance`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
      body: JSON.stringify({ enable }),
    }, 15000)
    if (res.ok) {
      const data = await res.json()
      return { success: true, message: data.message || 'Mode updated' }
    }
	throw await responseError(res, `Failed to update maintenance mode for ${nodeIP}`)
  } catch (err) {
	console.warn(`Maintenance API error for ${nodeIP}:`, err)
	throw err
  }
}

// ----------------------------------------------------
// 6. Proxmox VE Integration API (/api/proxmox/*)
// ----------------------------------------------------
export const MOCK_PROXMOX_STATUS: ProxmoxStatusResponse = {
  configured: true,
  node: 'pve',
  status: {
    node: 'pve',
    uptime: 1845200,
    cpu: 0.124,
    cpuUsagePercent: 12.4,
    cpuCores: 8,
    cpuModel: 'Intel Core i5-10400 CPU @ 2.90GHz',
    memory: {
      total: 34359738368, // 32 GB
      used: 17179869184, // 16 GB
      free: 17179869184, // 16 GB
      available: 17179869184,
      usagePercent: 50.0,
    },
    storage: {
      name: 'local-lvm',
      total: 512000000000, // 512 GB
      used: 153600000000, // 153.6 GB
      free: 358400000000, // 358.4 GB
      usagePercent: 30.0,
      type: 'lvmthin',
    },
  },
}

export const fetchProxmoxStatus = async (): Promise<ProxmoxStatusResponse> => {
  try {
    const res = await fetchWithTimeout('/api/proxmox/status')
    if (res.ok) {
      const data = await res.json()
      return data
    }
  } catch (err) {
    console.warn('Endpoint /api/proxmox/status not reachable, using fallback:', err)
  }

  return MOCK_PROXMOX_STATUS
}

export const fetchNextVMID = async (): Promise<number> => {
  try {
    const res = await fetchWithTimeout('/api/proxmox/next-vmid')
    if (res.ok) {
      const data = await res.json()
      if (typeof data.vmid === 'number') {
        return data.vmid
      }
    }
  } catch (err) {
    console.warn('Endpoint /api/proxmox/next-vmid not reachable, using fallback:', err)
  }

  return 113
}

export const createProxmoxWorker = async (
  params: CreateWorkerParams
): Promise<CreateWorkerResult> => {
  try {
    const res = await fetchWithTimeout('/api/proxmox/worker', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
      body: JSON.stringify(params),
    }, 120000)
    if (res.ok) {
      const data = await res.json()
      return data
    }
	throw await responseError(res, 'Failed to create worker VM')
  } catch (err: any) {
    if (err.name === 'AbortError') {
      throw new Error('Timeout waiting for worker VM creation')
    }
	throw err
  }
}

export const deleteProxmoxWorker = async (vmid: number): Promise<DeleteWorkerResult> => {
  try {
    const res = await fetchWithTimeout(`/api/proxmox/worker/${vmid}`, {
      method: 'DELETE',
	  headers: { ...getAuthHeaders() },
    }, 90000)
    if (res.ok) {
      const data = await res.json()
      return data
    }
	throw await responseError(res, `Failed to delete worker VM ${vmid}`)
  } catch (err: any) {
    console.warn(`Error deleting worker VM ${vmid}:`, err)
	throw err
  }
}

// ----------------------------------------------------
// 7. Telegram Alerting & Cluster Watcher API (/api/alerts/*)
// ----------------------------------------------------
export const MOCK_ALERTS_CONFIG: AlertsConfig = {
  enabled: true,
  bot_configured: true,
  bot_token_masked: '718293****',
  botToken: '718293****',
  chat_id_masked: '-100****4321',
  chatID: '-100****4321',
  min_level: 'WARNING',
  minLevel: 'WARNING',
  check_interval_seconds: 30,
  watcher_running: true,
  monitored_nodes: 3,
  active_alerts_count: 0,
  activeAlertsCount: 0,
  last_check_time: new Date().toISOString(),
  lastCheckTime: new Date().toISOString(),
  recent_alerts: [
    {
      id: 'mock-alert-1',
      level: 'INFO',
      title: 'TalosDeck Alerting Initialized',
      message: 'Background health watcher started monitoring 3 cluster nodes.',
      timestamp: new Date(Date.now() - 3600000).toISOString(),
      success: true,
    },
    {
      id: 'mock-alert-2',
      level: 'RECOVERED',
      title: 'Node talos-worker-2 Ready',
      message: 'Node communication re-established and healthy',
      timestamp: new Date(Date.now() - 1800000).toISOString(),
      success: true,
    },
  ],
}

export const fetchAlertsConfig = async (): Promise<AlertsConfig> => {
  try {
    const res = await fetchWithTimeout('/api/alerts/config', {
      headers: { ...getAuthHeaders() },
    })
    if (res.ok) {
      const data = await res.json()
      return data
    }
	if (res.status === 401) throw await responseError(res, 'Authentication required to read alert settings')
  } catch (err) {
	if (err instanceof Error && err.name === 'UnauthorizedError') throw err
    console.warn('Endpoint /api/alerts/config not reachable, using fallback:', err)
  }

  return MOCK_ALERTS_CONFIG
}

export const updateAlertsConfig = async (
  config: UpdateAlertsPayload
): Promise<{ success: boolean; message?: string; config?: AlertsConfig }> => {
  try {
    const res = await fetchWithTimeout('/api/alerts/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
      body: JSON.stringify(config),
    })
    if (res.ok) {
      const data = await res.json()
      return { success: true, message: 'Settings saved', config: data }
    }
	throw await responseError(res, 'Failed to update alerts config')
  } catch (err: any) {
	console.warn('Error updating alerts config:', err)
	throw err
  }
}

export const sendTestAlert = async (
  payload?: { bot_token?: string; chat_id?: string; message?: string }
): Promise<{ success: boolean; message: string }> => {
  try {
    const res = await fetchWithTimeout('/api/alerts/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
      body: JSON.stringify(payload || {}),
    }, 15000)
    const data = await res.json().catch(() => ({}))
    if (res.ok && data.success) {
      return { success: true, message: data.message || 'Test alert delivered successfully' }
    }
	if (res.status === 401) throw await responseError(res, 'Authentication required to send test alerts')
	throw new Error(data.error || `HTTP ${res.status}: Failed to send test alert`)
  } catch (err: any) {
	console.warn('Failed to send test alert:', err)
	throw err
  }
}

// ----------------------------------------------------
// 8. Authentication & RBAC (Phase 5)
// ----------------------------------------------------
export const TOKEN_STORAGE_KEY = 'talosdeck_token'
export const currentUser = ref<UserInfo>({ username: 'guest', role: 'viewer' })
export const isAuthenticated = ref<boolean>(false)

export const getAuthToken = (): string | null => {
  return localStorage.getItem(TOKEN_STORAGE_KEY)
}

export const getAuthHeaders = (): Record<string, string> => {
  const token = getAuthToken()
  if (token) {
    return { Authorization: `Bearer ${token}` }
  }
  return {}
}

export const login = async (
  password: string
): Promise<{ success: boolean; user?: UserInfo; error?: string }> => {
  try {
    const res = await fetchWithTimeout('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) {
      throw new Error(data.error || `HTTP ${res.status}: Login failed`)
    }
    if (data.token) {
      localStorage.setItem(TOKEN_STORAGE_KEY, data.token)
      isAuthenticated.value = true
      currentUser.value = data.user || { username: 'admin', role: 'admin' }
      return { success: true, user: currentUser.value }
    }
    throw new Error('No token received from backend')
  } catch (err: any) {
    console.warn('Login request failed:', err)
    return { success: false, error: err.message || 'Invalid password' }
  }
}

export const logout = async (): Promise<void> => {
  try {
    await fetchWithTimeout('/api/auth/logout', {
      method: 'POST',
      headers: { ...getAuthHeaders() },
    })
  } catch {
    // Ignore network failure on logout
  } finally {
    localStorage.removeItem(TOKEN_STORAGE_KEY)
    isAuthenticated.value = false
    currentUser.value = { username: 'guest', role: 'viewer' }
  }
}

export const getMe = async (): Promise<MeResponse> => {
  const token = getAuthToken()
  if (!token) {
    isAuthenticated.value = false
    currentUser.value = { username: 'guest', role: 'viewer' }
    return { authenticated: false, user: currentUser.value }
  }

  try {
    const res = await fetchWithTimeout('/api/auth/me', {
      headers: { ...getAuthHeaders() },
    })
    if (res.ok) {
      const data: MeResponse = await res.json()
      isAuthenticated.value = data.authenticated
      currentUser.value = data.user
      return data
    }
	if (res.status === 401) {
	  localStorage.removeItem(TOKEN_STORAGE_KEY)
	  isAuthenticated.value = false
	  currentUser.value = { username: 'guest', role: 'viewer' }
	}
  } catch (err) {
	console.warn('Endpoint /api/auth/me not reachable:', err)
  }

  return { authenticated: false, user: { username: 'guest', role: 'viewer' } }
}

// ----------------------------------------------------
// 9. Audit Trail API (Phase 5)
// ----------------------------------------------------
export const MOCK_AUDIT_LOGS: AuditLogEvent[] = [
  {
    id: 'aud-001',
    timestamp: new Date(Date.now() - 3 * 60 * 1000).toISOString(),
    action: 'auth.login',
    user: 'admin',
    ip: '192.168.88.100',
    status: 'success',
    details: { role: 'admin' },
  },
  {
    id: 'aud-002',
    timestamp: new Date(Date.now() - 12 * 60 * 1000).toISOString(),
    action: 'node.reboot',
    user: 'admin',
    ip: '192.168.88.100',
    status: 'success',
    details: { node: '10.42.0.111' },
  },
  {
    id: 'aud-003',
    timestamp: new Date(Date.now() - 35 * 60 * 1000).toISOString(),
    action: 'backup.create',
    user: 'admin',
    ip: '192.168.88.100',
    status: 'success',
    details: { type: 'etcd', id: 'etcd-10.42.0.110-snapshot', size: '2.3 MB' },
  },
  {
    id: 'aud-004',
    timestamp: new Date(Date.now() - 75 * 60 * 1000).toISOString(),
    action: 'node.maintenance',
    user: 'admin',
    ip: '192.168.88.100',
    status: 'success',
    details: { node: '10.42.0.112', enable: true },
  },
  {
    id: 'aud-005',
    timestamp: new Date(Date.now() - 120 * 60 * 1000).toISOString(),
    action: 'worker.create',
    user: 'admin',
    ip: '192.168.88.100',
    status: 'success',
    details: { vmid: 114, name: 'talos-worker-3', status: 'running' },
  },
]

export const fetchAuditLogs = async (
  limit = 50,
  action?: string,
  search?: string
): Promise<AuditLogEvent[]> => {
  try {
    const params = new URLSearchParams()
    if (limit) params.set('limit', String(limit))
    if (action) params.set('action', action)
    if (search) params.set('search', search)

    const res = await fetchWithTimeout(`/api/audit?${params.toString()}`, {
      headers: { ...getAuthHeaders() },
    })
    if (res.ok) {
      const data = await res.json()
      if (Array.isArray(data)) {
        return data
      }
    }
  } catch (err) {
    console.warn('Endpoint /api/audit not reachable, using mock fallback:', err)
  }

  let list = [...MOCK_AUDIT_LOGS]
  if (action) {
    list = list.filter((e) => e.action.toLowerCase().includes(action.toLowerCase()))
  }
  if (search) {
    const q = search.toLowerCase()
    list = list.filter(
      (e) =>
        e.action.toLowerCase().includes(q) ||
        e.user.toLowerCase().includes(q) ||
        e.ip.toLowerCase().includes(q) ||
        e.status.toLowerCase().includes(q) ||
        (e.details && JSON.stringify(e.details).toLowerCase().includes(q))
    )
  }
  return list.slice(0, limit)
}

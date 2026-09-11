<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Zap,
  ShieldCheck,
  RotateCw,
  Wrench,
  Stethoscope,
  RefreshCw,
  Server,
  Database,
  CheckCircle2,
  Play,
  Check,
  Sliders,
  Bell,
  Send,
  Eye,
  EyeOff,
  AlertTriangle,
  AlertOctagon,
  Info,
  Clock,
  Search,
  Filter,
  User,
  Globe,
  Activity,
  X,
  XCircle,
  FileText,
  Download,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type {
  NodeOverview,
  EtcdClusterHealth,
  BootstrapCheckItem,
  AlertsConfig,
  UpdateAlertsPayload,
  AuditLogEvent,
  BackupInfo,
} from '../../types'
import {
  fetchEtcdHealth,
  runBootstrapCheck,
  toggleMaintenanceMode,
  fetchAlertsConfig,
  updateAlertsConfig,
  sendTestAlert,
  fetchAuditLogs,
  rebootNode,
  waitForNodeReboot,
  fetchBackups,
  createBackup,
  downloadBackup,
  deleteBackup,
} from '../../api'

const props = defineProps<{
  nodes: NodeOverview[]
}>()

const emit = defineEmits<{
  (e: 'show-toast', payload: { message: string; type: 'success' | 'error' | 'info' }): void
}>()

const etcd = ref<EtcdClusterHealth | null>(null)
const loadingEtcd = ref(false)

// Telegram Alerting state
const alertsConfig = ref<AlertsConfig | null>(null)
const loadingAlerts = ref(false)
const savingAlerts = ref(false)
const testingAlerts = ref(false)

const botToken = ref('')
const showBotToken = ref(false)
const chatID = ref('')
const minLevel = ref('WARNING')
const alertsEnabled = ref(true)

// Sub-tabs navigation
const activeSubTab = ref<'overview' | 'audit'>('overview')

// Audit Trail state
const auditLogs = ref<AuditLogEvent[]>([])
const loadingAudit = ref(false)
const auditSearch = ref('')
const auditActionFilter = ref('all')

const loadAuditLogs = async () => {
  loadingAudit.value = true
  try {
    const action = auditActionFilter.value === 'all' ? '' : auditActionFilter.value
    auditLogs.value = await fetchAuditLogs(100, action, auditSearch.value)
  } catch (err) {
    console.error('Failed to load audit logs:', err)
  } finally {
    loadingAudit.value = false
  }
}

const filteredAuditLogs = computed(() => {
  let list = auditLogs.value
  if (auditActionFilter.value !== 'all') {
    list = list.filter((e) => e.action.toLowerCase().includes(auditActionFilter.value.toLowerCase()))
  }
  if (auditSearch.value.trim()) {
    const q = auditSearch.value.toLowerCase()
    list = list.filter(
      (e) =>
        e.action.toLowerCase().includes(q) ||
        e.user.toLowerCase().includes(q) ||
        e.ip.toLowerCase().includes(q) ||
        e.status.toLowerCase().includes(q) ||
        (e.details && JSON.stringify(e.details).toLowerCase().includes(q))
    )
  }
  return list
})

const getActionBadgeClass = (action: string) => {
  if (action.startsWith('auth.')) {
    return 'bg-amber-950/60 text-amber-300 border-amber-800/60'
  }
  if (action.startsWith('node.')) {
    return 'bg-sky-950/60 text-sky-300 border-sky-800/60'
  }
  if (action.startsWith('backup.')) {
    return 'bg-violet-950/60 text-violet-300 border-violet-800/60'
  }
  if (action.startsWith('worker.')) {
    return 'bg-orange-950/60 text-orange-300 border-orange-800/60'
  }
  return 'bg-zinc-800 text-zinc-300 border-zinc-700'
}

const formatTimestamp = (ts: string) => {
  if (!ts) return '-'
  try {
    const d = new Date(ts)
    return d.toLocaleString()
  } catch {
    return ts
  }
}

// Maintenance mode state
const targetNodeIP = ref<string>('')
const maintenanceState = ref<Record<string, boolean>>({})
const maintenanceLoading = ref(false)

// Rolling reboot state
const isRollingOpen = ref(false)
const rollingInProgress = ref(false)
const rollingStep = ref(0)
const rollingError = ref('')
const rollingNodes = computed<string[]>(() => {
  if (!props.nodes || props.nodes.length === 0) return []
  return props.nodes.map((n) => n.hostname || n.ip)
})

// Bootstrap check state
const checkingBootstrap = ref(false)
const bootstrapResults = ref<BootstrapCheckItem[]>([])
const bootstrapPassedCount = computed(() => bootstrapResults.value.filter((item) => item.status === 'success').length)
const bootstrapHasErrors = computed(() => bootstrapResults.value.some((item) => item.status === 'error'))

// Backup and disaster recovery state
const backups = ref<BackupInfo[]>([])
const loadingBackups = ref(false)
const creatingBackup = ref<'full' | 'etcd' | null>(null)
const deletingBackupID = ref('')

const loadBackups = async () => {
  loadingBackups.value = true
  try {
    backups.value = await fetchBackups()
  } catch (err) {
    console.error('Failed to load backups:', err)
  } finally {
    loadingBackups.value = false
  }
}

const handleCreateBackup = async (type: 'full' | 'etcd') => {
  creatingBackup.value = type
  try {
    const controlPlane = props.nodes.find((node) => node.role === 'controlplane')
    const backup = await createBackup(type, type === 'etcd' ? controlPlane?.ip || '' : '')
    backups.value = [backup, ...backups.value.filter((item) => item.id !== backup.id)]
    emit('show-toast', { message: `Backup ${backup.filename} created`, type: 'success' })
    loadAuditLogs()
  } catch (err: any) {
    emit('show-toast', { message: err?.message || 'Failed to create backup', type: 'error' })
  } finally {
    creatingBackup.value = null
  }
}

const handleDownloadBackup = async (backup: BackupInfo) => {
  try {
    await downloadBackup(backup)
  } catch (err: any) {
    emit('show-toast', { message: err?.message || 'Failed to download backup', type: 'error' })
  }
}

const handleDeleteBackup = async (backup: BackupInfo) => {
  if (!window.confirm(`Delete backup ${backup.filename}?`)) return
  deletingBackupID.value = backup.id
  try {
    await deleteBackup(backup.id)
    backups.value = backups.value.filter((item) => item.id !== backup.id)
    emit('show-toast', { message: `Backup ${backup.filename} deleted`, type: 'success' })
    loadAuditLogs()
  } catch (err: any) {
    emit('show-toast', { message: err?.message || 'Failed to delete backup', type: 'error' })
  } finally {
    deletingBackupID.value = ''
  }
}

const formatBackupTime = (timestamp: string) => {
  const date = new Date(timestamp)
  return Number.isNaN(date.getTime()) ? timestamp : date.toLocaleString()
}

const loadEtcd = async () => {
  loadingEtcd.value = true
  try {
    etcd.value = await fetchEtcdHealth()
  } catch (err) {
    console.error('Failed to load etcd health:', err)
  } finally {
    loadingEtcd.value = false
  }
}

const loadAlertsConfig = async () => {
  loadingAlerts.value = true
  try {
    const data = await fetchAlertsConfig()
    alertsConfig.value = data
    alertsEnabled.value = data.enabled
    minLevel.value = data.min_level || data.minLevel || 'WARNING'
    chatID.value = data.chat_id || data.chatID || ''
  } catch (err) {
    console.error('Failed to load alerts config:', err)
  } finally {
    loadingAlerts.value = false
  }
}

const handleSaveAlerts = async () => {
  savingAlerts.value = true
  try {
    const payload: UpdateAlertsPayload = {
      enabled: alertsEnabled.value,
      min_level: minLevel.value,
    }
    if (chatID.value.trim() && !chatID.value.includes('*')) {
      payload.chat_id = chatID.value.trim()
    }
    if (botToken.value.trim() && !botToken.value.includes('*')) {
      payload.bot_token = botToken.value.trim()
    }
    const res = await updateAlertsConfig(payload)
    if (res.success) {
      botToken.value = ''
      emit('show-toast', { message: t('alerts_saved_toast'), type: 'success' })
      await loadAlertsConfig()
    } else {
      emit('show-toast', { message: res.message || 'Error saving config', type: 'error' })
    }
  } catch (err: any) {
    emit('show-toast', { message: err.message || 'Error saving config', type: 'error' })
  } finally {
    savingAlerts.value = false
  }
}

const handleSendTestAlert = async () => {
  testingAlerts.value = true
  try {
    const payload: { bot_token?: string; chat_id?: string; message?: string } = {
      message: 'Test notification triggered from TalosDeck Control Plane',
    }
    if (botToken.value.trim() && !botToken.value.includes('*')) {
      payload.bot_token = botToken.value.trim()
    }
    if (chatID.value.trim() && !chatID.value.includes('*')) {
      payload.chat_id = chatID.value.trim()
    }
    const res = await sendTestAlert(payload)
    emit('show-toast', { message: res.message || t('alerts_test_success_toast'), type: 'success' })
    await loadAlertsConfig()
  } catch (err: any) {
    emit('show-toast', { message: err.message || t('alerts_test_fail_toast'), type: 'error' })
  } finally {
    testingAlerts.value = false
  }
}

const formatAlertTime = (timestamp?: string | null): string => {
  if (!timestamp) return t('alerts_never_checked')
  try {
    const d = new Date(timestamp)
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  } catch {
    return String(timestamp)
  }
}

const formatTimeAgo = (timestamp?: string): string => {
  if (!timestamp) return ''
  try {
    const diff = Math.floor((Date.now() - new Date(timestamp).getTime()) / 1000)
    if (diff < 30) return t('alerts_time_ago_just_now')
    if (diff < 60) return `${diff} ${t('alerts_time_ago_sec')}`
    const mins = Math.floor(diff / 60)
    if (mins < 60) return `${mins} ${t('alerts_time_ago_min')}`
    const hours = Math.floor(mins / 60)
    return `${hours}h ago`
  } catch {
    return ''
  }
}

onMounted(() => {
  loadEtcd()
  loadAlertsConfig()
  loadAuditLogs()
  loadBackups()
  if (props.nodes.length > 0) {
    targetNodeIP.value = props.nodes[0].ip
  }
})

// Run Bootstrap Check
const startBootstrapCheck = async () => {
  checkingBootstrap.value = true
  bootstrapResults.value = []
	try {
		bootstrapResults.value = await runBootstrapCheck()
		const allPassed = bootstrapResults.value.every((item) => item.status === 'success')
		emit('show-toast', {
			message: allPassed ? t('ops_check_passed') : `${bootstrapPassedCount.value}/${bootstrapResults.value.length} checks passed`,
			type: allPassed ? 'success' : bootstrapHasErrors.value ? 'error' : 'info',
		})
  } catch (err) {
    console.error('Bootstrap check failed', err)
  } finally {
    checkingBootstrap.value = false
  }
}

// Toggle Maintenance
const handleToggleMaintenance = async () => {
  if (props.nodes.length === 0 || !targetNodeIP.value) {
    emit('show-toast', {
      message: t('ops_maintenance_no_target') || 'No nodes available for maintenance',
      type: 'error',
    })
    return
  }
  maintenanceLoading.value = true
  const current = Boolean(maintenanceState.value[targetNodeIP.value])
  const next = !current
  try {
    const res = await toggleMaintenanceMode(targetNodeIP.value, next)
    maintenanceState.value[targetNodeIP.value] = next
    emit('show-toast', { message: res.message, type: 'success' })
    loadAuditLogs()
  } catch {
    emit('show-toast', { message: 'Failed to toggle mode', type: 'error' })
  } finally {
    maintenanceLoading.value = false
  }
}

// Start Rolling Reboot
const startRollingReboot = async () => {
	if (rollingNodes.value.length === 0) return
	const nodes = [...props.nodes]
	rollingInProgress.value = true
	rollingStep.value = 1
	rollingError.value = ''
	try {
		for (let i = 0; i < nodes.length; i++) {
			rollingStep.value = i + 1
			await rebootNode(nodes[i].ip)
			await waitForNodeReboot(nodes[i].ip)
		}
		rollingStep.value = nodes.length + 1
		isRollingOpen.value = false
		emit('show-toast', { message: t('ops_rolling_success'), type: 'success' })
	} catch (err: any) {
		rollingError.value = err?.message || 'Rolling reboot failed'
		emit('show-toast', { message: rollingError.value, type: 'error' })
	} finally {
		rollingInProgress.value = false
	}
}
</script>

<template>
  <div class="space-y-4">
    <!-- Header -->
    <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
      <div>
        <div class="flex items-center gap-2.5">
          <h2 class="text-sm font-semibold text-zinc-100 flex items-center gap-2">
            <Zap class="w-5 h-5 text-amber-400" />
            <span>{{ t('operations_title') }}</span>
          </h2>
          <span class="px-2 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-800 text-emerald-400 border border-zinc-700/60 flex items-center gap-1.5">
            <span class="w-1.5 h-1.5 rounded-full bg-emerald-400 "></span>
            <span>etcd Quorum OK</span>
          </span>
        </div>
        <p class="text-xs text-zinc-400 mt-1">
          {{ t('operations_subtitle') }}
        </p>
      </div>

      <button
        @click="activeSubTab === 'audit' ? loadAuditLogs() : loadEtcd()"
        :disabled="activeSubTab === 'audit' ? loadingAudit : loadingEtcd"
        class="flex items-center gap-1.5 px-3 py-2 rounded-md bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
        :title="t('refresh')"
      >
        <RefreshCw :class="['w-3.5 h-3.5 text-orange-400', (activeSubTab === 'audit' ? loadingAudit : loadingEtcd) ? 'animate-spin' : '']" />
        <span class="hidden sm:inline">{{ (activeSubTab === 'audit' ? loadingAudit : loadingEtcd) ? t('refreshing') : t('refresh') }}</span>
      </button>
    </div>

    <!-- Sub Navigation Tabs -->
    <div class="flex items-center gap-2 border-b border-zinc-800 pb-3">
      <button
        @click="activeSubTab = 'overview'"
        :class="[
          'flex items-center gap-2 px-3.5 py-2 rounded-md text-xs font-semibold transition-all cursor-pointer border',
          activeSubTab === 'overview'
            ? 'bg-zinc-800 text-zinc-100 border-zinc-700 '
            : 'text-zinc-400 hover:text-zinc-200 bg-zinc-950/60 border-transparent hover:bg-zinc-900',
        ]"
      >
        <Zap class="w-3.5 h-3.5 text-amber-400" />
        <span>{{ t('audit_tab_ops') }}</span>
      </button>

      <button
        @click="activeSubTab = 'audit'"
        :class="[
          'flex items-center gap-2 px-3.5 py-2 rounded-md text-xs font-semibold transition-all cursor-pointer border',
          activeSubTab === 'audit'
            ? 'bg-zinc-800 text-zinc-100 border-zinc-700 '
            : 'text-zinc-400 hover:text-zinc-200 bg-zinc-950/60 border-transparent hover:bg-zinc-900',
        ]"
      >
        <ShieldCheck class="w-3.5 h-3.5 text-emerald-400" />
        <span>{{ t('audit_tab_logs') }}</span>
        <span
          v-if="auditLogs.length > 0"
          class="px-2 py-0.5 rounded-full text-[10px] font-mono bg-zinc-900 text-emerald-300 border border-emerald-800/60"
        >
          {{ auditLogs.length }}
        </span>
      </button>
    </div>

    <!-- Overview SubTab Content -->
    <div v-show="activeSubTab === 'overview'" class="space-y-4">
      <!-- etcd Health Overview Card -->
      <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-5  space-y-4 ">
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pb-3 border-b border-zinc-800/70">
        <div class="flex items-center gap-3">
          <div :class="['p-2.5 rounded-md border', etcd?.healthy ? 'bg-emerald-950/60 text-emerald-400 border-emerald-800/60' : 'bg-red-950/60 text-red-400 border-red-800/60']">
            <Database class="w-5 h-5" />
          </div>
          <div>
            <h3 class="text-sm font-semibold text-zinc-100 flex items-center gap-2">
              <span>{{ t('etcd_health_title') }}</span>
              <span :class="['text-xs font-semibold px-2 py-0.5 rounded border', etcd?.healthy ? 'bg-emerald-950/80 text-emerald-400 border-emerald-800/60' : 'bg-red-950/80 text-red-400 border-red-800/60']">
                {{ etcd?.healthy ? t('etcd_quorum_ok') : 'Degraded' }}
              </span>
            </h3>
            <p class="text-xs text-zinc-400 mt-0.5">
              {{ t('etcd_leader') }}: <span class="text-zinc-200 font-mono font-semibold">{{ etcd?.leaderName || 'Not elected' }}</span> (ID: {{ etcd?.leaderId || '—' }})
            </p>
          </div>
        </div>

        <div class="flex items-center gap-2 text-xs font-mono">
          <div class="px-2.5 py-1 rounded-lg bg-zinc-950 text-zinc-400 border border-zinc-800">
            Raft Term: <span class="text-zinc-200 font-bold">{{ etcd?.raftTerm ?? 0 }}</span>
          </div>
          <div class="px-2.5 py-1 rounded-lg bg-zinc-950 text-zinc-400 border border-zinc-800">
            DB Size: <span class="text-orange-300 font-bold">{{ etcd?.totalDbSize || '—' }}</span>
          </div>
        </div>
      </div>

      <!-- Alarms Banner -->
      <div :class="['rounded-md p-3 border flex items-center justify-between gap-3 text-xs', etcd?.alarms?.length ? 'bg-red-950/30 border-red-800/60' : 'bg-zinc-950/70 border-zinc-800']">
        <div class="flex items-center gap-2.5">
          <ShieldCheck class="w-4 h-4 text-emerald-400 shrink-0" />
          <span class="text-zinc-300">{{ etcd?.alarms?.length ? 'Active etcd alarms' : t('etcd_no_alarms') }}</span>
        </div>
        <span class="text-[11px] font-mono text-zinc-500">{{ etcd?.alarms?.length || 0 }} active alarms</span>
      </div>

      <!-- Members Table -->
      <div class="overflow-x-auto rounded-md border border-zinc-800/70 bg-zinc-950/50">
        <table class="w-full text-left text-xs">
          <thead>
            <tr class="border-b border-zinc-800 text-[11px] text-zinc-400 uppercase tracking-wider bg-zinc-900/80">
              <th class="py-2.5 px-4 font-semibold">{{ t('etcd_col_member') }}</th>
              <th class="py-2.5 px-3 font-semibold">{{ t('etcd_col_role') }}</th>
              <th class="py-2.5 px-3 font-semibold">{{ t('etcd_col_endpoints') }}</th>
              <th class="py-2.5 px-3 font-semibold">{{ t('etcd_col_dbsize') }}</th>
              <th class="py-2.5 px-3 font-semibold">{{ t('etcd_col_status') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/50 font-mono">
            <tr
              v-for="member in etcd?.members || []"
              :key="member.id"
              class="hover:bg-zinc-800/30 transition-colors"
            >
              <!-- Member name & id -->
              <td class="py-3 px-4">
                <div class="font-bold text-zinc-100 flex items-center gap-2">
                  <Server class="w-3.5 h-3.5 text-orange-400" />
                  <span>{{ member.name }}</span>
                </div>
                <span class="text-[10px] text-zinc-500 block mt-0.5">ID: {{ member.id }}</span>
              </td>

              <!-- Role -->
              <td class="py-3 px-3">
                <span
                  :class="[
                    'px-2 py-0.5 rounded text-[11px] font-semibold tracking-wide border',
                    member.leader
                      ? 'bg-amber-950/60 text-amber-300 border-amber-800/60'
                      : 'bg-zinc-900 text-zinc-400 border-zinc-800',
                  ]"
                >
                  {{ member.leader ? 'Leader' : 'Follower' }}
                </span>
              </td>

              <!-- Endpoints -->
              <td class="py-3 px-3 text-zinc-400 text-[11px]">
                <div>Peer: <span class="text-zinc-300">{{ member.peerURLs[0] }}</span></div>
                <div>Client: <span class="text-zinc-300">{{ member.clientURLs[0] }}</span></div>
              </td>

              <!-- DB size -->
              <td class="py-3 px-3 text-orange-300 font-bold">
                {{ member.dbSize }}
              </td>

              <!-- Health -->
              <td class="py-3 px-3">
                <span class="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[11px] font-semibold bg-emerald-950/60 text-emerald-400 border border-emerald-800/60">
                  <CheckCircle2 class="w-3 h-3" />
                  <span>Healthy</span>
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Telegram Alerting & Cluster Health Monitor Panel (Phase 5) -->
    <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-5  space-y-5 ">
      <!-- Panel Header -->
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pb-3 border-b border-zinc-800/70">
        <div class="flex items-center gap-3">
          <div class="p-2.5 rounded-md bg-sky-950/60 text-sky-400 border border-sky-800/60 shadow-[0_0_12px_rgba(56,189,248,0.25)]">
            <Bell class="w-5 h-5" />
          </div>
          <div>
            <h3 class="text-sm font-semibold text-zinc-100 flex items-center gap-2">
              <span>{{ t('alerts_panel_title') }}</span>
              <span
                :class="[
                  'text-xs font-semibold px-2 py-0.5 rounded border flex items-center gap-1.5',
                  alertsConfig?.enabled && alertsConfig?.watcher_running
                    ? 'bg-emerald-950/80 text-emerald-400 border-emerald-800/60'
                    : 'bg-zinc-800 text-zinc-400 border-zinc-700/60',
                ]"
              >
                <span
                  v-if="alertsConfig?.enabled && alertsConfig?.watcher_running"
                  class="w-1.5 h-1.5 rounded-full bg-emerald-400 "
                ></span>
                <span>{{ alertsConfig?.enabled && alertsConfig?.watcher_running ? t('alerts_status_active') : t('alerts_status_disabled') }}</span>
              </span>
            </h3>
            <p class="text-xs text-zinc-400 mt-0.5">
              {{ t('alerts_panel_subtitle') }}
            </p>
          </div>
        </div>

        <div class="flex flex-wrap items-center gap-2 text-xs font-mono">
          <div class="px-2.5 py-1 rounded-lg bg-zinc-950 text-zinc-400 border border-zinc-800 flex items-center gap-1.5">
            <Clock class="w-3.5 h-3.5 text-zinc-500" />
            <span>{{ t('alerts_last_check') }}:</span>
            <span class="text-zinc-200 font-bold">
              {{ formatAlertTime(alertsConfig?.last_check_time || alertsConfig?.lastCheckTime) }}
            </span>
          </div>
          <div
            :class="[
              'px-2.5 py-1 rounded-lg border font-bold',
              (alertsConfig?.active_alerts_count ?? alertsConfig?.activeAlertsCount ?? 0) > 0
                ? 'bg-rose-950/60 text-rose-300 border-rose-800/60'
                : 'bg-zinc-950 text-emerald-400 border-zinc-800',
            ]"
          >
            {{ alertsConfig?.active_alerts_count ?? alertsConfig?.activeAlertsCount ?? 0 }} {{ t('alerts_active_issues') }}
          </div>
          <button
            @click="loadAlertsConfig"
            :disabled="loadingAlerts"
            class="p-1.5 rounded-lg bg-zinc-950 text-zinc-400 hover:text-zinc-200 border border-zinc-800 hover:border-zinc-700 transition-colors cursor-pointer"
            :title="t('refresh')"
          >
            <RefreshCw :class="['w-3.5 h-3.5 text-sky-400', loadingAlerts ? 'animate-spin' : '']" />
          </button>
        </div>
      </div>

      <!-- Main Panel Grid: Form (Left) & Recent Alerts (Right) -->
      <div class="grid grid-cols-1 lg:grid-cols-12 gap-5">
        <!-- Configuration Form (7 cols) -->
        <div class="lg:col-span-7 space-y-4">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
            <!-- Bot Token -->
            <div class="sm:col-span-2 space-y-1.5">
              <div class="flex items-center justify-between">
                <label for="alerts-bot-token" class="text-xs font-semibold text-zinc-300">
                  {{ t('alerts_bot_token') }}
                </label>
                <span class="text-[11px] font-mono text-zinc-500">
                  {{ alertsConfig?.bot_configured ? t('alerts_bot_configured') : t('alerts_bot_not_configured') }}
                </span>
              </div>
              <div class="relative">
                <input
                  id="alerts-bot-token"
                  :type="showBotToken ? 'text' : 'password'"
                  v-model="botToken"
                  :placeholder="alertsConfig?.bot_token_masked ? (showBotToken ? alertsConfig.bot_token_masked : '••••••••••••••••••••') : t('alerts_bot_token_placeholder')"
                  class="w-full bg-zinc-950 border border-zinc-800 focus:border-sky-500 rounded-md px-3 py-2 text-xs text-zinc-100 font-mono focus:outline-none transition-colors pr-10"
                />
                <button
                  type="button"
                  @click="showBotToken = !showBotToken"
                  class="absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-500 hover:text-zinc-300 transition-colors cursor-pointer"
                  :title="showBotToken ? t('alerts_hide_token') : t('alerts_show_token')"
                >
                  <EyeOff v-if="showBotToken" class="w-4 h-4" />
                  <Eye v-else class="w-4 h-4" />
                </button>
              </div>
            </div>

            <!-- Chat ID -->
            <div class="space-y-1.5">
              <label for="alerts-chat-id" class="text-xs font-semibold text-zinc-300">
                {{ t('alerts_chat_id') }}
              </label>
              <input
                id="alerts-chat-id"
                type="text"
                v-model="chatID"
                :placeholder="alertsConfig?.chat_id_masked || '-1001234567890'"
                class="w-full bg-zinc-950 border border-zinc-800 focus:border-sky-500 rounded-md px-3 py-2 text-xs text-zinc-100 font-mono focus:outline-none transition-colors"
              />
              <p class="text-[10px] text-zinc-500">
                {{ t('alerts_chat_id_hint') }}
              </p>
            </div>

            <!-- Minimum Level -->
            <div class="space-y-1.5">
              <label for="alerts-min-level" class="text-xs font-semibold text-zinc-300">
                {{ t('alerts_min_level') }}
              </label>
              <select
                id="alerts-min-level"
                v-model="minLevel"
                class="w-full bg-zinc-950 border border-zinc-800 focus:border-sky-500 rounded-md px-3 py-2 text-xs text-zinc-100 focus:outline-none transition-colors cursor-pointer"
              >
                <option value="INFO">{{ t('alerts_level_info') }}</option>
                <option value="WARNING">{{ t('alerts_level_warning') }}</option>
                <option value="CRITICAL">{{ t('alerts_level_critical') }}</option>
              </select>
            </div>
          </div>

          <!-- Enable Switch -->
          <div class="flex items-center justify-between p-3 rounded-md bg-zinc-950/60 border border-zinc-800/80">
            <div class="flex items-center gap-2.5">
              <input
                id="alerts-enabled-checkbox"
                type="checkbox"
                v-model="alertsEnabled"
                class="w-4 h-4 text-sky-500 bg-zinc-900 border-zinc-700 rounded focus:ring-sky-500 focus:ring-1 cursor-pointer"
              />
              <label for="alerts-enabled-checkbox" class="text-xs font-semibold text-zinc-200 cursor-pointer">
                {{ t('alerts_enable_switch') }}
              </label>
            </div>
            <span class="text-[11px] font-mono text-zinc-500">
              {{ t('alerts_check_interval') }}: {{ alertsConfig?.check_interval_seconds ?? 30 }}s
            </span>
          </div>

          <!-- Buttons -->
          <div class="flex flex-wrap items-center gap-3 pt-1">
            <button
              @click="handleSaveAlerts"
              :disabled="savingAlerts"
              class="flex items-center justify-center gap-2 px-4 py-2 rounded-md bg-sky-600 hover:bg-sky-500 active:scale-95 text-xs font-semibold text-white transition-all cursor-pointer disabled:opacity-50  shadow-sky-950/50"
            >
              <RefreshCw v-if="savingAlerts" class="w-3.5 h-3.5 animate-spin" />
              <Check v-else class="w-3.5 h-3.5" />
              <span>{{ savingAlerts ? t('alerts_saving') : t('alerts_save_btn') }}</span>
            </button>

            <button
              @click="handleSendTestAlert"
              :disabled="testingAlerts"
              class="flex items-center justify-center gap-2 px-4 py-2 rounded-md bg-zinc-800 hover:bg-zinc-700 active:scale-95 border border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
            >
              <RefreshCw v-if="testingAlerts" class="w-3.5 h-3.5 animate-spin text-sky-400" />
              <Send v-else class="w-3.5 h-3.5 text-sky-400" />
              <span>{{ testingAlerts ? t('alerts_testing') : t('alerts_test_btn') }}</span>
            </button>
          </div>
        </div>

        <!-- Recent Alerts History (5 cols) -->
        <div class="lg:col-span-5 flex flex-col space-y-2">
          <div class="flex items-center justify-between">
            <h4 class="text-xs font-bold uppercase tracking-wider text-zinc-400 flex items-center gap-2">
              <span>{{ t('alerts_recent_title') }}</span>
              <span class="px-1.5 py-0.2 rounded-full text-[10px] font-mono bg-zinc-800 text-zinc-300">
                {{ (alertsConfig?.recent_alerts || alertsConfig?.recentAlerts)?.length || 0 }}
              </span>
            </h4>
          </div>

          <!-- Alerts List Container -->
          <div
            v-if="!alertsConfig?.recent_alerts?.length && !alertsConfig?.recentAlerts?.length"
            class="h-[260px] rounded-md border border-zinc-800/80 bg-zinc-950/50 p-4 flex flex-col items-center justify-center text-center space-y-2.5"
          >
            <ShieldCheck class="w-7 h-7 text-emerald-400/80" />
            <p class="text-xs text-zinc-400 leading-relaxed max-w-[280px]">
              {{ t('alerts_recent_empty') }}
            </p>
          </div>

          <div
            v-else
            class="h-[260px] overflow-y-auto space-y-2 pr-1 rounded-md border border-zinc-800/70 bg-zinc-950/40 p-2.5 custom-scrollbar"
          >
            <div
              v-for="alert in (alertsConfig?.recent_alerts || alertsConfig?.recentAlerts || [])"
              :key="alert.id"
              class="p-2.5 rounded-md bg-zinc-900/90 border border-zinc-800/80 hover:border-zinc-700/80 transition-all space-y-1 text-xs"
            >
              <div class="flex items-start justify-between gap-2">
                <div class="flex items-center gap-1.5 font-bold text-zinc-200">
                  <!-- Level Icon -->
                  <AlertOctagon
                    v-if="alert.level === 'CRITICAL'"
                    class="w-3.5 h-3.5 text-rose-400 shrink-0"
                  />
                  <AlertTriangle
                    v-else-if="alert.level === 'WARNING'"
                    class="w-3.5 h-3.5 text-amber-400 shrink-0"
                  />
                  <CheckCircle2
                    v-else-if="alert.level === 'RECOVERED'"
                    class="w-3.5 h-3.5 text-emerald-400 shrink-0"
                  />
                  <Info
                    v-else
                    class="w-3.5 h-3.5 text-orange-400 shrink-0"
                  />
                  <span class="truncate">{{ alert.title }}</span>
                </div>

                <!-- Level Badge -->
                <span
                  :class="[
                    'text-[10px] font-semibold px-1.5 py-0.2 rounded font-mono shrink-0 border',
                    alert.level === 'CRITICAL'
                      ? 'bg-rose-950/80 text-rose-300 border-rose-800/60'
                      : alert.level === 'WARNING'
                      ? 'bg-amber-950/80 text-amber-300 border-amber-800/60'
                      : alert.level === 'RECOVERED'
                      ? 'bg-emerald-950/80 text-emerald-300 border-emerald-800/60'
                      : 'bg-orange-950/80 text-orange-300 border-orange-800/60',
                  ]"
                >
                  {{ alert.level }}
                </span>
              </div>

              <!-- Message preview -->
              <p class="text-[11px] text-zinc-400 font-mono whitespace-pre-line line-clamp-2">
                {{ alert.message }}
              </p>

              <!-- Timestamp and status -->
              <div class="flex items-center justify-between pt-1 text-[10px] text-zinc-500 font-mono border-t border-zinc-800/50">
                <span>{{ formatAlertTime(alert.timestamp) }} • {{ formatTimeAgo(alert.timestamp) }}</span>
                <span v-if="alert.success" class="text-emerald-400">Delivered</span>
                <span v-else class="text-rose-400">Failed</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Backup & Disaster Recovery -->
    <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-5 space-y-4 ">
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pb-3 border-b border-zinc-800/70">
        <div class="flex items-center gap-3">
          <div class="p-2.5 rounded-md bg-violet-950/60 text-violet-400 border border-violet-800/60">
            <FileText class="w-5 h-5" />
          </div>
          <div>
            <h3 class="text-sm font-semibold text-zinc-100">Backup & Disaster Recovery</h3>
            <p class="text-xs text-zinc-400 mt-0.5">Etcd snapshots and full cluster recovery archives</p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          <button
            @click="handleCreateBackup('etcd')"
            :disabled="creatingBackup !== null"
            class="px-3 py-2 rounded-md bg-zinc-800 hover:bg-zinc-700 border border-zinc-700 text-xs font-semibold text-zinc-200 disabled:opacity-50 cursor-pointer"
          >
            {{ creatingBackup === 'etcd' ? 'Creating…' : 'Etcd snapshot' }}
          </button>
          <button
            @click="handleCreateBackup('full')"
            :disabled="creatingBackup !== null"
            class="px-3 py-2 rounded-md bg-violet-600 hover:bg-violet-500 text-xs font-semibold text-white disabled:opacity-50 cursor-pointer"
          >
            {{ creatingBackup === 'full' ? 'Creating…' : 'Full backup' }}
          </button>
          <button @click="loadBackups" :disabled="loadingBackups" class="p-2 rounded-md bg-zinc-950 border border-zinc-800 text-zinc-400 hover:text-zinc-200 disabled:opacity-50 cursor-pointer" title="Refresh backups">
            <RefreshCw :class="['w-3.5 h-3.5', loadingBackups ? 'animate-spin' : '']" />
          </button>
        </div>
      </div>

      <div v-if="backups.length === 0 && !loadingBackups" class="py-6 text-center text-xs text-zinc-500">
        No backups created yet
      </div>
      <div v-else class="overflow-x-auto rounded-md border border-zinc-800/70">
        <table class="w-full text-left text-xs">
          <thead class="bg-zinc-950/70 text-zinc-400 uppercase text-[10px] tracking-wider">
            <tr>
              <th class="px-3 py-2.5">Backup</th>
              <th class="px-3 py-2.5">Type</th>
              <th class="px-3 py-2.5">Created</th>
              <th class="px-3 py-2.5">Size</th>
              <th class="px-3 py-2.5 text-right">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/60">
            <tr v-for="backup in backups" :key="backup.id" class="hover:bg-zinc-800/30">
              <td class="px-3 py-2.5 font-mono text-orange-300">{{ backup.filename }}</td>
              <td class="px-3 py-2.5 text-zinc-300 uppercase">{{ backup.type }}</td>
              <td class="px-3 py-2.5 text-zinc-400">{{ formatBackupTime(backup.timestamp) }}</td>
              <td class="px-3 py-2.5 font-mono text-zinc-300">{{ backup.humanSize }}</td>
              <td class="px-3 py-2.5">
                <div class="flex justify-end gap-2">
                  <button @click="handleDownloadBackup(backup)" class="p-1.5 rounded-lg bg-orange-950/60 border border-orange-800/50 text-orange-300 hover:bg-orange-900/60 cursor-pointer" title="Download backup">
                    <Download class="w-3.5 h-3.5" />
                  </button>
                  <button @click="handleDeleteBackup(backup)" :disabled="deletingBackupID === backup.id" class="p-1.5 rounded-lg bg-red-950/60 border border-red-800/50 text-red-300 hover:bg-red-900/60 disabled:opacity-50 cursor-pointer" title="Delete backup">
                    <XCircle class="w-3.5 h-3.5" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Quick Operations Grid -->
    <div class="space-y-3">
      <h3 class="text-sm font-semibold text-zinc-100 flex items-center gap-2">
        <Sliders class="w-4 h-4 text-orange-400" />
        <span>{{ t('ops_quick_actions') }}</span>
      </h3>

      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <!-- Operation 1: Rolling Reboot -->
        <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-5 flex flex-col justify-between space-y-4 hover:border-zinc-700/80 transition-all ">
          <div class="space-y-2">
            <div class="p-2.5 rounded-md bg-violet-950/60 text-violet-400 border border-violet-800/50 w-fit">
              <RotateCw class="w-5 h-5" />
            </div>
            <h4 class="text-sm font-bold text-zinc-100">{{ t('ops_rolling_reboot') }}</h4>
            <p class="text-xs text-zinc-400 leading-relaxed">
              {{ t('ops_rolling_reboot_desc') }}
            </p>
          </div>

          <button
            @click="isRollingOpen = true"
            class="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-md bg-violet-950/70 hover:bg-violet-900/80 border border-violet-800/70 text-xs font-semibold text-violet-200 transition-all cursor-pointer active:scale-95 "
          >
            <Play class="w-3.5 h-3.5" />
            <span>{{ t('ops_rolling_reboot') }}</span>
          </button>
        </div>

        <!-- Operation 2: Maintenance Mode Toggle -->
        <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-5 flex flex-col justify-between space-y-4 hover:border-zinc-700/80 transition-all ">
          <div class="space-y-2">
            <div class="p-2.5 rounded-md bg-amber-950/60 text-amber-400 border border-amber-800/50 w-fit">
              <Wrench class="w-5 h-5" />
            </div>
            <h4 class="text-sm font-bold text-zinc-100">{{ t('ops_maintenance_mode') }}</h4>
            <p class="text-xs text-zinc-400 leading-relaxed">
              {{ t('ops_maintenance_desc') }}
            </p>

            <!-- Node Selector -->
            <div class="pt-2">
              <label for="ops-target-node" class="text-[11px] text-zinc-400 block mb-1 font-medium">
                {{ t('ops_select_node_target') }}:
              </label>
              <select
                id="ops-target-node"
                v-model="targetNodeIP"
                class="w-full bg-zinc-950 border border-zinc-800 rounded-md px-2.5 py-1.5 text-xs text-zinc-200 font-mono focus:outline-none focus:border-amber-500/70 cursor-pointer"
              >
                <option
                  v-for="node in nodes"
                  :key="node.ip"
                  :value="node.ip"
                >
                  {{ node.hostname }} ({{ maintenanceState[node.ip] ? t('ops_cordoned_status') : t('ops_active_status') }})
                </option>
              </select>
            </div>
          </div>

          <button
            @click="handleToggleMaintenance"
            :disabled="maintenanceLoading || props.nodes.length === 0"
            class="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-md bg-amber-950/70 hover:bg-amber-900/80 border border-amber-800/70 text-xs font-semibold text-amber-200 transition-all cursor-pointer active:scale-95 disabled:opacity-50 "
          >
            <Wrench class="w-3.5 h-3.5" />
            <span>{{ maintenanceLoading ? t('loading') : t('ops_apply_maintenance') }}</span>
          </button>
        </div>

        <!-- Operation 3: Bootstrap Check Diagnostics -->
        <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-5 flex flex-col justify-between space-y-4 hover:border-zinc-700/80 transition-all ">
          <div class="space-y-2">
            <div class="p-2.5 rounded-md bg-orange-950/60 text-orange-400 border border-orange-800/50 w-fit">
              <Stethoscope class="w-5 h-5" />
            </div>
            <h4 class="text-sm font-bold text-zinc-100">{{ t('ops_bootstrap_check') }}</h4>
            <p class="text-xs text-zinc-400 leading-relaxed">
              {{ t('ops_bootstrap_desc') }}
            </p>
          </div>

          <button
            @click="startBootstrapCheck"
            :disabled="checkingBootstrap"
            class="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-md bg-orange-950/70 hover:bg-orange-900/80 border border-orange-800/70 text-xs font-semibold text-orange-200 transition-all cursor-pointer active:scale-95 disabled:opacity-50 "
          >
            <RefreshCw :class="['w-3.5 h-3.5 text-orange-400', checkingBootstrap ? 'animate-spin' : '']" />
            <span>{{ checkingBootstrap ? t('ops_checking') : t('ops_run_check') }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Bootstrap Diagnostics Results (if run) -->
    <div
      v-if="bootstrapResults.length > 0"
      :class="['bg-zinc-900/90 border rounded-lg p-5 space-y-3.5  ', bootstrapHasErrors ? 'border-red-800/50 shadow-red-950/20' : 'border-orange-800/50 shadow-orange-950/20']"
    >
      <div class="flex items-center justify-between pb-2 border-b border-zinc-800">
        <h4 class="text-sm font-bold text-zinc-100 flex items-center gap-2">
          <XCircle v-if="bootstrapHasErrors" class="w-4 h-4 text-red-400" />
          <CheckCircle2 v-else class="w-4 h-4 text-emerald-400" />
          <span>{{ t('ops_bootstrap_check') }}: {{ bootstrapPassedCount }}/{{ bootstrapResults.length }} passed</span>
        </h4>
        <span class="text-xs font-mono text-zinc-500">{{ bootstrapPassedCount }}/{{ bootstrapResults.length }} passed</span>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
        <div
          v-for="item in bootstrapResults"
          :key="item.id"
          class="p-3 rounded-md bg-zinc-950/70 border border-zinc-800 flex items-start gap-3"
        >
          <div :class="['p-1.5 rounded-lg border shrink-0 mt-0.5', item.status === 'success' ? 'bg-emerald-950/80 text-emerald-400 border-emerald-800/60' : item.status === 'warning' ? 'bg-amber-950/80 text-amber-400 border-amber-800/60' : 'bg-red-950/80 text-red-400 border-red-800/60']">
            <Check v-if="item.status === 'success'" class="w-3.5 h-3.5" />
            <AlertTriangle v-else-if="item.status === 'warning'" class="w-3.5 h-3.5" />
            <XCircle v-else class="w-3.5 h-3.5" />
          </div>
          <div>
            <span class="font-bold text-zinc-200 block">{{ item.title }}</span>
            <p class="text-[11px] text-zinc-400 mt-0.5">{{ item.description }}</p>
            <p v-if="item.detail" class="text-[10px] font-mono text-orange-400/90 mt-1">
              {{ item.detail }}
            </p>
          </div>
        </div>
      </div>
    </div>
    <!-- End Overview SubTab Content -->

    <!-- Audit Trail SubTab Content -->
    <div v-show="activeSubTab === 'audit'" class="space-y-4">
      <!-- Search and Filter Bar -->
      <div class="bg-[#11151a] border border-[#252c34] rounded-lg p-4 flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3 ">
        <!-- Search Input -->
        <div class="relative flex-1">
          <Search class="w-4 h-4 text-zinc-500 absolute left-3.5 top-1/2 -translate-y-1/2" />
          <input
            type="text"
            v-model="auditSearch"
            :placeholder="t('audit_search')"
            class="w-full bg-zinc-950 border border-zinc-800 rounded-md pl-9 pr-3.5 py-2 text-xs text-zinc-200 placeholder:text-zinc-600 focus:outline-none focus:border-orange-500/70 transition-colors"
          />
        </div>

        <!-- Action Filter & Refresh -->
        <div class="flex items-center gap-2 shrink-0">
          <div class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-zinc-950 border border-zinc-800 text-xs">
            <Filter class="w-3.5 h-3.5 text-zinc-400" />
            <select
              v-model="auditActionFilter"
              class="bg-transparent text-zinc-200 focus:outline-none cursor-pointer font-medium"
            >
              <option value="all" class="bg-zinc-900 text-zinc-200">{{ t('audit_filter_all') }}</option>
              <option value="auth.login" class="bg-zinc-900 text-zinc-200">auth.login</option>
              <option value="auth.logout" class="bg-zinc-900 text-zinc-200">auth.logout</option>
              <option value="node.reboot" class="bg-zinc-900 text-zinc-200">node.reboot</option>
              <option value="node.maintenance" class="bg-zinc-900 text-zinc-200">node.maintenance</option>
              <option value="backup.create" class="bg-zinc-900 text-zinc-200">backup.create</option>
              <option value="backup.delete" class="bg-zinc-900 text-zinc-200">backup.delete</option>
              <option value="worker.create" class="bg-zinc-900 text-zinc-200">worker.create</option>
              <option value="worker.delete" class="bg-zinc-900 text-zinc-200">worker.delete</option>
            </select>
          </div>

          <button
            @click="loadAuditLogs"
            :disabled="loadingAudit"
            class="p-2 rounded-md bg-zinc-950 border border-zinc-800 hover:border-zinc-700 text-zinc-300 hover:text-white transition-colors cursor-pointer"
            :title="t('refresh')"
          >
            <RefreshCw :class="['w-3.5 h-3.5 text-orange-400', loadingAudit ? 'animate-spin' : '']" />
          </button>
        </div>
      </div>

      <!-- Audit Events Table Card -->
      <div class="bg-[#11151a] border border-[#252c34] rounded-lg overflow-hidden ">
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead>
              <tr class="border-b border-zinc-800 text-[11px] text-zinc-400 uppercase tracking-wider bg-zinc-900/90">
                <th class="py-3 px-4 font-semibold">{{ t('audit_col_time') }}</th>
                <th class="py-3 px-3 font-semibold">{{ t('audit_col_user') }}</th>
                <th class="py-3 px-3 font-semibold">{{ t('audit_col_ip') }}</th>
                <th class="py-3 px-3 font-semibold">{{ t('audit_col_action') }}</th>
                <th class="py-3 px-3 font-semibold">{{ t('audit_col_status') }}</th>
                <th class="py-3 px-4 font-semibold">{{ t('audit_col_details') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/50 font-mono">
              <tr
                v-for="ev in filteredAuditLogs"
                :key="ev.id"
                class="hover:bg-zinc-800/30 transition-colors"
              >
                <!-- Time -->
                <td class="py-3 px-4 whitespace-nowrap text-zinc-400 text-[11px]">
                  <div class="flex items-center gap-1.5 font-sans">
                    <Clock class="w-3.5 h-3.5 text-zinc-500 shrink-0" />
                    <span>{{ formatTimestamp(ev.timestamp) }}</span>
                  </div>
                </td>

                <!-- User -->
                <td class="py-3 px-3 whitespace-nowrap">
                  <span
                    :class="[
                      'inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-semibold border',
                      ev.user === 'admin'
                        ? 'bg-emerald-950/60 text-emerald-300 border-emerald-800/60'
                        : 'bg-zinc-800/80 text-zinc-300 border-zinc-700',
                    ]"
                  >
                    <User class="w-3 h-3" />
                    <span>{{ ev.user }}</span>
                  </span>
                </td>

                <!-- IP -->
                <td class="py-3 px-3 whitespace-nowrap text-zinc-400 text-[11px]">
                  <div class="flex items-center gap-1">
                    <Globe class="w-3 h-3 text-zinc-600 shrink-0" />
                    <span>{{ ev.ip || '127.0.0.1' }}</span>
                  </div>
                </td>

                <!-- Action -->
                <td class="py-3 px-3 whitespace-nowrap">
                  <span
                    :class="[
                      'inline-flex items-center gap-1 px-2.5 py-0.5 rounded-md text-[11px] font-semibold border font-mono tracking-tight',
                      getActionBadgeClass(ev.action),
                    ]"
                  >
                    <Activity class="w-3 h-3 shrink-0" />
                    <span>{{ ev.action }}</span>
                  </span>
                </td>

                <!-- Status -->
                <td class="py-3 px-3 whitespace-nowrap">
                  <span
                    v-if="ev.status === 'success'"
                    class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-semibold bg-emerald-950/60 text-emerald-400 border border-emerald-800/60"
                  >
                    <CheckCircle2 class="w-3 h-3" />
                    <span>{{ t('audit_status_success') }}</span>
                  </span>
                  <span
                    v-else
                    class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-semibold bg-red-950/60 text-red-400 border border-red-800/60"
                  >
                    <XCircle class="w-3 h-3" />
                    <span>{{ t('audit_status_failed') }}</span>
                  </span>
                </td>

                <!-- Details -->
                <td class="py-3 px-4 text-xs font-sans text-zinc-300">
                  <div v-if="ev.details && Object.keys(ev.details).length > 0" class="flex flex-wrap gap-1.5">
                    <span
                      v-for="(val, key) in ev.details"
                      :key="key"
                      class="px-1.5 py-0.5 rounded bg-zinc-950 border border-zinc-800 font-mono text-[10px] text-zinc-300"
                    >
                      <span class="text-zinc-500">{{ key }}:</span> {{ val }}
                    </span>
                  </div>
                  <span v-else class="text-zinc-600 text-[11px] font-mono">-</span>
                </td>
              </tr>

              <!-- Empty State -->
              <tr v-if="filteredAuditLogs.length === 0">
                <td colspan="6" class="py-12 text-center text-zinc-500 font-sans">
                  <FileText class="w-8 h-8 mx-auto mb-2 text-zinc-600 opacity-60" />
                  <p class="text-xs font-medium">{{ t('audit_empty') }}</p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Footer Stats Bar -->
        <div class="px-4 py-2.5 border-t border-zinc-800/80 bg-zinc-950/60 text-[11px] text-zinc-400 flex items-center justify-between">
          <div class="flex items-center gap-4">
            <span>{{ t('audit_total_events') }}: <strong class="text-zinc-200 font-mono">{{ filteredAuditLogs.length }}</strong></span>
            <span>•</span>
            <span class="text-emerald-400 font-medium">{{ t('audit_stat_success') }}: <strong class="font-mono">{{ filteredAuditLogs.filter(e => e.status === 'success').length }}</strong></span>
            <span>•</span>
            <span class="text-red-400 font-medium">{{ t('audit_stat_failed') }}: <strong class="font-mono">{{ filteredAuditLogs.filter(e => e.status === 'failed').length }}</strong></span>
          </div>
          <span class="font-mono text-zinc-600 text-[10px]">data/audit.log</span>
        </div>
      </div>
    </div>

    <!-- Rolling Reboot Confirmation / Progress Modal -->
    <div
      v-if="isRollingOpen"
      @click.self="!rollingInProgress && (isRollingOpen = false)"
      class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/80  animate-fade-in"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="rolling-reboot-modal-title"
        class="w-full max-w-md bg-zinc-900 border border-zinc-800 rounded-lg p-6  space-y-5 relative"
      >
        <!-- Close Button -->
        <button
          v-if="!rollingInProgress"
          @click="isRollingOpen = false"
          class="absolute top-4 right-4 p-1.5 rounded-lg text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800 transition-colors cursor-pointer"
          :title="t('close')"
          :aria-label="t('close')"
        >
          <X class="w-5 h-5" />
        </button>

        <div class="flex items-center gap-3">
          <div class="p-3 rounded-md bg-violet-950/60 text-violet-400 border border-violet-800/60">
            <RotateCw class="w-5 h-5" />
          </div>
          <div>
            <h3 id="rolling-reboot-modal-title" class="text-sm font-semibold text-zinc-100">{{ t('ops_rolling_confirm_title') }}</h3>
            <p class="text-xs text-zinc-400 mt-0.5">Cluster Rolling Upgrade & Reboot</p>
          </div>
        </div>

        <p class="text-xs text-zinc-300 leading-relaxed bg-zinc-950 p-3.5 rounded-md border border-zinc-800/80">
          {{ t('ops_rolling_confirm_text') }}
        </p>

        <!-- Empty State if no nodes -->
        <div v-if="!rollingInProgress && rollingNodes.length === 0" class="p-3.5 rounded-md bg-amber-950/30 border border-amber-900/40 text-xs text-amber-300">
          {{ t('node_empty_title') || 'No nodes available' }}
        </div>

		<div v-if="rollingError" class="p-3.5 rounded-md bg-red-950/30 border border-red-900/40 text-xs text-red-300">
			{{ rollingError }}
		</div>

        <!-- Progress Steps if running -->
        <div v-if="rollingInProgress" class="space-y-2 py-2">
          <p class="text-xs text-orange-300 font-semibold flex items-center gap-2">
            <RefreshCw class="w-3.5 h-3.5 animate-spin" />
            <span>{{ t('ops_rolling_in_progress') }}</span>
          </p>
          <div class="space-y-1.5">
            <div
              v-for="(n, idx) in rollingNodes"
              :key="n"
              class="flex items-center justify-between text-xs px-3 py-1.5 rounded-lg bg-zinc-950 border border-zinc-800 font-mono"
            >
              <span class="text-zinc-300">{{ n }}</span>
              <span v-if="rollingStep > idx + 1" class="text-emerald-400 flex items-center gap-1 font-sans text-[11px]">
                <Check class="w-3 h-3" /> Done
              </span>
              <span v-else-if="rollingStep === idx + 1" class="text-amber-400 flex items-center gap-1 font-sans text-[11px]">
                <RefreshCw class="w-3 h-3 animate-spin" /> Rebooting...
              </span>
              <span v-else class="text-zinc-600 font-sans text-[11px]">Waiting</span>
            </div>
          </div>
        </div>

        <!-- Buttons -->
        <div class="flex items-center justify-end gap-3 pt-2">
          <button
            @click="isRollingOpen = false"
            :disabled="rollingInProgress"
            :aria-label="t('reboot_cancel_btn')"
            class="px-4 py-2 rounded-md bg-zinc-800 hover:bg-zinc-700 text-xs font-semibold text-zinc-300 transition-all cursor-pointer disabled:opacity-50"
          >
            {{ t('reboot_cancel_btn') }}
          </button>
          <button
            @click="startRollingReboot"
            :disabled="rollingInProgress || rollingNodes.length === 0"
            :aria-label="t('ops_start_rolling')"
            class="flex items-center gap-1.5 px-4 py-2 rounded-md bg-violet-600 hover:bg-violet-500 text-xs font-semibold text-white transition-all cursor-pointer disabled:opacity-50  shadow-violet-950/50"
          >
            <Play class="w-3.5 h-3.5" />
            <span>{{ t('ops_start_rolling') }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</div>
</template>

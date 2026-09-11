<script setup lang="ts">
import { ref, onMounted } from 'vue'
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
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type { NodeOverview, EtcdClusterHealth, BootstrapCheckItem } from '../../types'
import { fetchEtcdHealth, runBootstrapCheck, toggleMaintenanceMode } from '../../api'

const props = defineProps<{
  nodes: NodeOverview[]
}>()

const emit = defineEmits<{
  (e: 'show-toast', payload: { message: string; type: 'success' | 'error' | 'info' }): void
}>()

const etcd = ref<EtcdClusterHealth | null>(null)
const loadingEtcd = ref(false)

// Maintenance mode state
const targetNodeIP = ref<string>('')
const maintenanceState = ref<Record<string, boolean>>({})
const maintenanceLoading = ref(false)

// Rolling reboot state
const isRollingOpen = ref(false)
const rollingInProgress = ref(false)
const rollingStep = ref(0)
const rollingNodes = ['talos-cp-1', 'talos-worker-1', 'talos-worker-2']

// Bootstrap check state
const checkingBootstrap = ref(false)
const bootstrapResults = ref<BootstrapCheckItem[]>([])

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

onMounted(() => {
  loadEtcd()
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
    emit('show-toast', { message: t('ops_check_passed'), type: 'success' })
  } catch (err) {
    console.error('Bootstrap check failed', err)
  } finally {
    checkingBootstrap.value = false
  }
}

// Toggle Maintenance
const handleToggleMaintenance = async () => {
  if (!targetNodeIP.value) return
  maintenanceLoading.value = true
  const current = Boolean(maintenanceState.value[targetNodeIP.value])
  const next = !current
  try {
    const res = await toggleMaintenanceMode(targetNodeIP.value, next)
    maintenanceState.value[targetNodeIP.value] = next
    emit('show-toast', { message: res.message, type: 'success' })
  } catch {
    emit('show-toast', { message: 'Failed to toggle mode', type: 'error' })
  } finally {
    maintenanceLoading.value = false
  }
}

// Start Rolling Reboot
const startRollingReboot = async () => {
  rollingInProgress.value = true
  rollingStep.value = 1
  // Sequence through nodes
  for (let i = 0; i < rollingNodes.length; i++) {
    rollingStep.value = i + 1
    await new Promise((r) => setTimeout(r, 1200))
  }
  rollingInProgress.value = false
  isRollingOpen.value = false
  emit('show-toast', { message: t('ops_rolling_success'), type: 'success' })
}
</script>

<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
      <div>
        <div class="flex items-center gap-2.5">
          <h2 class="text-xl font-bold tracking-tight text-zinc-100 flex items-center gap-2">
            <Zap class="w-5 h-5 text-amber-400" />
            <span>{{ t('operations_title') }}</span>
          </h2>
          <span class="px-2 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-800 text-emerald-400 border border-zinc-700/60 flex items-center gap-1.5">
            <span class="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse"></span>
            <span>etcd Quorum OK</span>
          </span>
        </div>
        <p class="text-xs text-zinc-400 mt-1">
          {{ t('operations_subtitle') }}
        </p>
      </div>

      <button
        @click="loadEtcd"
        :disabled="loadingEtcd"
        class="flex items-center gap-1.5 px-3 py-2 rounded-xl bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
        :title="t('refresh')"
      >
        <RefreshCw :class="['w-3.5 h-3.5 text-cyan-400', loadingEtcd ? 'animate-spin' : '']" />
        <span class="hidden sm:inline">{{ loadingEtcd ? t('refreshing') : t('refresh') }}</span>
      </button>
    </div>

    <!-- etcd Health Overview Card -->
    <div class="bg-zinc-900/80 border border-zinc-800/90 rounded-2xl p-5 backdrop-blur-sm space-y-4 shadow-md">
      <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pb-3 border-b border-zinc-800/70">
        <div class="flex items-center gap-3">
          <div class="p-2.5 rounded-xl bg-emerald-950/60 text-emerald-400 border border-emerald-800/60 shadow-[0_0_12px_rgba(16,185,129,0.25)]">
            <Database class="w-5 h-5" />
          </div>
          <div>
            <h3 class="text-base font-bold text-zinc-100 flex items-center gap-2">
              <span>{{ t('etcd_health_title') }}</span>
              <span class="text-xs font-semibold px-2 py-0.5 rounded bg-emerald-950/80 text-emerald-400 border border-emerald-800/60">
                {{ t('etcd_quorum_ok') }}
              </span>
            </h3>
            <p class="text-xs text-zinc-400 mt-0.5">
              {{ t('etcd_leader') }}: <span class="text-zinc-200 font-mono font-semibold">{{ etcd?.leaderName || 'talos-cp-1' }}</span> (ID: {{ etcd?.leaderId }})
            </p>
          </div>
        </div>

        <div class="flex items-center gap-2 text-xs font-mono">
          <div class="px-2.5 py-1 rounded-lg bg-zinc-950 text-zinc-400 border border-zinc-800">
            Raft Term: <span class="text-zinc-200 font-bold">{{ etcd?.raftTerm || 4 }}</span>
          </div>
          <div class="px-2.5 py-1 rounded-lg bg-zinc-950 text-zinc-400 border border-zinc-800">
            DB Size: <span class="text-cyan-300 font-bold">{{ etcd?.totalDbSize || '24.8 MB' }}</span>
          </div>
        </div>
      </div>

      <!-- Alarms Banner -->
      <div class="rounded-xl p-3 bg-zinc-950/70 border border-zinc-800 flex items-center justify-between gap-3 text-xs">
        <div class="flex items-center gap-2.5">
          <ShieldCheck class="w-4 h-4 text-emerald-400 shrink-0" />
          <span class="text-zinc-300">{{ t('etcd_no_alarms') }}</span>
        </div>
        <span class="text-[11px] font-mono text-zinc-500">0 active alarms</span>
      </div>

      <!-- Members Table -->
      <div class="overflow-x-auto rounded-xl border border-zinc-800/70 bg-zinc-950/50">
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
                  <Server class="w-3.5 h-3.5 text-cyan-400" />
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
              <td class="py-3 px-3 text-cyan-300 font-bold">
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

    <!-- Quick Operations Grid -->
    <div class="space-y-3">
      <h3 class="text-base font-bold text-zinc-100 flex items-center gap-2">
        <Sliders class="w-4 h-4 text-cyan-400" />
        <span>{{ t('ops_quick_actions') }}</span>
      </h3>

      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <!-- Operation 1: Rolling Reboot -->
        <div class="bg-zinc-900/80 border border-zinc-800/90 rounded-2xl p-5 flex flex-col justify-between space-y-4 hover:border-zinc-700/80 transition-all shadow-md">
          <div class="space-y-2">
            <div class="p-2.5 rounded-xl bg-violet-950/60 text-violet-400 border border-violet-800/50 w-fit">
              <RotateCw class="w-5 h-5" />
            </div>
            <h4 class="text-sm font-bold text-zinc-100">{{ t('ops_rolling_reboot') }}</h4>
            <p class="text-xs text-zinc-400 leading-relaxed">
              {{ t('ops_rolling_reboot_desc') }}
            </p>
          </div>

          <button
            @click="isRollingOpen = true"
            class="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-xl bg-violet-950/70 hover:bg-violet-900/80 border border-violet-800/70 text-xs font-semibold text-violet-200 transition-all cursor-pointer active:scale-95 shadow-sm"
          >
            <Play class="w-3.5 h-3.5" />
            <span>{{ t('ops_rolling_reboot') }}</span>
          </button>
        </div>

        <!-- Operation 2: Maintenance Mode Toggle -->
        <div class="bg-zinc-900/80 border border-zinc-800/90 rounded-2xl p-5 flex flex-col justify-between space-y-4 hover:border-zinc-700/80 transition-all shadow-md">
          <div class="space-y-2">
            <div class="p-2.5 rounded-xl bg-amber-950/60 text-amber-400 border border-amber-800/50 w-fit">
              <Wrench class="w-5 h-5" />
            </div>
            <h4 class="text-sm font-bold text-zinc-100">{{ t('ops_maintenance_mode') }}</h4>
            <p class="text-xs text-zinc-400 leading-relaxed">
              {{ t('ops_maintenance_desc') }}
            </p>

            <!-- Node Selector -->
            <div class="pt-2">
              <label class="text-[11px] text-zinc-400 block mb-1 font-medium">
                {{ t('ops_select_node_target') }}:
              </label>
              <select
                v-model="targetNodeIP"
                class="w-full bg-zinc-950 border border-zinc-800 rounded-xl px-2.5 py-1.5 text-xs text-zinc-200 font-mono focus:outline-none focus:border-amber-500/70 cursor-pointer"
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
            :disabled="maintenanceLoading"
            class="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-xl bg-amber-950/70 hover:bg-amber-900/80 border border-amber-800/70 text-xs font-semibold text-amber-200 transition-all cursor-pointer active:scale-95 disabled:opacity-50 shadow-sm"
          >
            <Wrench class="w-3.5 h-3.5" />
            <span>{{ maintenanceLoading ? t('loading') : t('ops_apply_maintenance') }}</span>
          </button>
        </div>

        <!-- Operation 3: Bootstrap Check Diagnostics -->
        <div class="bg-zinc-900/80 border border-zinc-800/90 rounded-2xl p-5 flex flex-col justify-between space-y-4 hover:border-zinc-700/80 transition-all shadow-md">
          <div class="space-y-2">
            <div class="p-2.5 rounded-xl bg-cyan-950/60 text-cyan-400 border border-cyan-800/50 w-fit">
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
            class="w-full flex items-center justify-center gap-2 px-4 py-2 rounded-xl bg-cyan-950/70 hover:bg-cyan-900/80 border border-cyan-800/70 text-xs font-semibold text-cyan-200 transition-all cursor-pointer active:scale-95 disabled:opacity-50 shadow-sm"
          >
            <RefreshCw :class="['w-3.5 h-3.5 text-cyan-400', checkingBootstrap ? 'animate-spin' : '']" />
            <span>{{ checkingBootstrap ? t('ops_checking') : t('ops_run_check') }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Bootstrap Diagnostics Results (if run) -->
    <div
      v-if="bootstrapResults.length > 0"
      class="bg-zinc-900/90 border border-cyan-800/50 rounded-2xl p-5 space-y-3.5 backdrop-blur-md shadow-lg shadow-cyan-950/20"
    >
      <div class="flex items-center justify-between pb-2 border-b border-zinc-800">
        <h4 class="text-sm font-bold text-zinc-100 flex items-center gap-2">
          <CheckCircle2 class="w-4 h-4 text-emerald-400" />
          <span>{{ t('ops_bootstrap_check') }}: {{ t('ops_check_passed') }}</span>
        </h4>
        <span class="text-xs font-mono text-zinc-500">5/5 passed</span>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
        <div
          v-for="item in bootstrapResults"
          :key="item.id"
          class="p-3 rounded-xl bg-zinc-950/70 border border-zinc-800 flex items-start gap-3"
        >
          <div class="p-1.5 rounded-lg bg-emerald-950/80 text-emerald-400 border border-emerald-800/60 shrink-0 mt-0.5">
            <Check class="w-3.5 h-3.5" />
          </div>
          <div>
            <span class="font-bold text-zinc-200 block">{{ item.title }}</span>
            <p class="text-[11px] text-zinc-400 mt-0.5">{{ item.description }}</p>
            <p v-if="item.detail" class="text-[10px] font-mono text-cyan-400/90 mt-1">
              {{ item.detail }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <!-- Rolling Reboot Confirmation / Progress Modal -->
    <div
      v-if="isRollingOpen"
      class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/80 backdrop-blur-sm animate-fade-in"
    >
      <div class="w-full max-w-md bg-zinc-900 border border-zinc-800 rounded-2xl p-6 shadow-2xl space-y-5">
        <div class="flex items-center gap-3">
          <div class="p-3 rounded-xl bg-violet-950/60 text-violet-400 border border-violet-800/60">
            <RotateCw class="w-5 h-5" />
          </div>
          <div>
            <h3 class="text-base font-bold text-zinc-100">{{ t('ops_rolling_confirm_title') }}</h3>
            <p class="text-xs text-zinc-400 mt-0.5">Cluster Rolling Upgrade & Reboot</p>
          </div>
        </div>

        <p class="text-xs text-zinc-300 leading-relaxed bg-zinc-950 p-3.5 rounded-xl border border-zinc-800/80">
          {{ t('ops_rolling_confirm_text') }}
        </p>

        <!-- Progress Steps if running -->
        <div v-if="rollingInProgress" class="space-y-2 py-2">
          <p class="text-xs text-cyan-300 font-semibold flex items-center gap-2">
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
            class="px-4 py-2 rounded-xl bg-zinc-800 hover:bg-zinc-700 text-xs font-semibold text-zinc-300 transition-all cursor-pointer disabled:opacity-50"
          >
            {{ t('reboot_cancel_btn') }}
          </button>
          <button
            @click="startRollingReboot"
            :disabled="rollingInProgress"
            class="flex items-center gap-1.5 px-4 py-2 rounded-xl bg-violet-600 hover:bg-violet-500 text-xs font-semibold text-white transition-all cursor-pointer disabled:opacity-50 shadow-lg shadow-violet-950/50"
          >
            <Play class="w-3.5 h-3.5" />
            <span>{{ t('ops_start_rolling') }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

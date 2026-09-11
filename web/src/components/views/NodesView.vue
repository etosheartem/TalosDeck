<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Server,
  Search,
  Shield,
  Cpu,
  Plus,
  HardDrive,
  Layers,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import { fetchProxmoxStatus } from '../../api'
import type { NodeOverview, ProxmoxStatusResponse, CreateWorkerResult } from '../../types'
import NodeCard from '../NodeCard.vue'
import AddWorkerModal from '../AddWorkerModal.vue'

const props = defineProps<{
  nodes: NodeOverview[]
}>()

const emit = defineEmits<{
  (e: 'open-services', node: NodeOverview): void
  (e: 'open-logs', node: NodeOverview): void
  (e: 'open-reboot', node: NodeOverview): void
  (e: 'refresh'): void
  (e: 'show-toast', payload: { message: string; type: 'success' | 'error' | 'info' }): void
}>()

const searchQuery = ref('')
const roleFilter = ref<'all' | 'controlplane' | 'worker'>('all')
const isAddModalOpen = ref(false)

// Proxmox VE Host Status
const proxmox = ref<ProxmoxStatusResponse | null>(null)
const loadingProxmox = ref(false)

const loadProxmox = async () => {
  loadingProxmox.value = true
  try {
    proxmox.value = await fetchProxmoxStatus()
  } catch (err) {
    console.warn('Failed to load Proxmox status in NodesView:', err)
  } finally {
    loadingProxmox.value = false
  }
}

onMounted(() => {
  loadProxmox()
})

const isProxmoxConfigured = computed(() => {
  return proxmox.value?.configured ?? false
})

const pveHostFreeRAM = computed(() => {
  const mem = proxmox.value?.status?.memory
  if (!mem) return '16.0 GB'
  const freeBytes = mem.available ?? mem.free ?? 0
  return `${(freeBytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
})

const pveHostRAMPercent = computed(() => {
  return Math.round(proxmox.value?.status?.memory?.usagePercent ?? 50)
})

const pveHostFreeDisk = computed(() => {
  const stg = proxmox.value?.status?.storage
  if (!stg) return '358.4 GB'
  return `${(stg.free / (1024 * 1024 * 1024)).toFixed(1)} GB`
})

const pveHostDiskPercent = computed(() => {
  return Math.round(proxmox.value?.status?.storage?.usagePercent ?? 30)
})

const pveHostCPU = computed(() => {
  return (proxmox.value?.status?.cpuUsagePercent ?? 12.4).toFixed(1)
})

const filteredNodes = computed(() => {
  return props.nodes.filter((node) => {
    const q = searchQuery.value.trim().toLowerCase()
    const matchesQuery = !q || node.hostname.toLowerCase().includes(q) || node.ip.includes(q)
    if (!matchesQuery) return false

    if (roleFilter.value !== 'all' && node.role !== roleFilter.value) {
      return false
    }

    return true
  })
})

const handleWorkerCreated = (result: CreateWorkerResult) => {
  isAddModalOpen.value = false
  emit('show-toast', {
    message: `${t('add_worker_success')}: ${result.name} (VMID ${result.vmid})`,
    type: 'success',
  })
  emit('refresh')
  loadProxmox()
}

const handleWorkerError = (err: string) => {
  emit('show-toast', {
    message: err || t('add_worker_error'),
    type: 'error',
  })
}
</script>

<template>
  <div class="space-y-5">
    <!-- Proxmox VE Host Status Card / Scale-Out Banner -->
    <div
      v-if="isProxmoxConfigured"
      class="rounded-2xl bg-gradient-to-r from-zinc-900/90 via-zinc-900/70 to-cyan-950/20 border border-zinc-800/90 p-4 sm:p-5 shadow-lg relative overflow-hidden"
    >
      <!-- Background Ambient Glow -->
      <div class="absolute -right-16 -top-16 w-48 h-48 bg-cyan-500/5 rounded-full blur-3xl pointer-events-none" />

      <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-4 relative z-10">
        <!-- Host Info Left Column -->
        <div class="flex items-center gap-3.5">
          <div class="w-11 h-11 rounded-xl bg-cyan-500/10 border border-cyan-500/30 flex items-center justify-center text-cyan-400 shrink-0 shadow-inner">
            <Server class="w-6 h-6" />
          </div>
          <div>
            <div class="flex items-center gap-2">
              <span class="text-sm font-bold text-zinc-100 tracking-tight">
                {{ t('proxmox_title') }}
              </span>
              <span class="font-mono text-xs text-cyan-300 font-semibold px-2 py-0.5 rounded bg-cyan-950/80 border border-cyan-800/50">
                {{ proxmox?.node || 'pve' }}
              </span>
              <span class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-medium bg-emerald-950/70 border border-emerald-800/60 text-emerald-400">
                <span class="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse" />
                {{ t('proxmox_status_connected') }}
              </span>
            </div>
            <p class="text-xs text-zinc-400 mt-1">
              {{ t('proxmox_scale_desc') }}
            </p>
          </div>
        </div>

        <!-- Host Live Metrics Grid -->
        <div class="flex flex-wrap items-center gap-3 sm:gap-4 text-xs">
          <!-- CPU Badge -->
          <div class="flex items-center gap-2 px-3 py-1.5 rounded-xl bg-zinc-950/70 border border-zinc-800/70">
            <Cpu class="w-3.5 h-3.5 text-emerald-400" />
            <div class="space-y-0.5">
              <div class="text-[10px] text-zinc-400 font-medium leading-none">{{ t('proxmox_cpu_load') }}</div>
              <div class="font-mono font-bold text-emerald-300 leading-none">{{ pveHostCPU }}%</div>
            </div>
          </div>

          <!-- Free RAM Badge -->
          <div class="flex items-center gap-2 px-3 py-1.5 rounded-xl bg-zinc-950/70 border border-zinc-800/70">
            <Layers class="w-3.5 h-3.5 text-indigo-400" />
            <div class="space-y-0.5">
              <div class="text-[10px] text-zinc-400 font-medium leading-none">{{ t('proxmox_free_ram') }}</div>
              <div class="flex items-center gap-1.5 leading-none">
                <span class="font-mono font-bold text-indigo-300">{{ pveHostFreeRAM }}</span>
                <span class="text-[10px] text-zinc-500">({{ pveHostRAMPercent }}%)</span>
              </div>
            </div>
          </div>

          <!-- Free Disk Badge -->
          <div class="flex items-center gap-2 px-3 py-1.5 rounded-xl bg-zinc-950/70 border border-zinc-800/70">
            <HardDrive class="w-3.5 h-3.5 text-fuchsia-400" />
            <div class="space-y-0.5">
              <div class="text-[10px] text-zinc-400 font-medium leading-none">{{ t('proxmox_free_disk') }}</div>
              <div class="flex items-center gap-1.5 leading-none">
                <span class="font-mono font-bold text-fuchsia-300">{{ pveHostFreeDisk }}</span>
                <span class="text-[10px] text-zinc-500">({{ pveHostDiskPercent }}%)</span>
              </div>
            </div>
          </div>

          <!-- Scale Out Action Button in Banner -->
          <button
            @click="isAddModalOpen = true"
            class="flex items-center gap-1.5 px-3.5 py-2 rounded-xl bg-gradient-to-r from-cyan-600 to-emerald-600 hover:from-cyan-500 hover:to-emerald-500 text-white text-xs font-semibold shadow-md shadow-cyan-950/40 transition-all cursor-pointer active:scale-95 ml-auto lg:ml-0"
          >
            <Plus class="w-3.5 h-3.5" />
            <span>{{ t('proxmox_add_worker_btn') }}</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Controls / Filter Bar -->
    <div class="flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3 pt-1">
      <!-- Section Heading -->
      <div class="flex items-center gap-2.5">
        <h2 class="text-lg font-bold text-zinc-100 tracking-tight">
          {{ t('tab_nodes') }}
        </h2>
        <span class="px-2 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-800 text-zinc-300 border border-zinc-700/60">
          {{ filteredNodes.length }}
        </span>
      </div>

      <!-- Filters: Search + Role Pills + Add Worker Button -->
      <div class="flex flex-wrap items-center gap-2.5">
        <!-- Search -->
        <div class="relative min-w-[200px] flex-1 sm:flex-none">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-zinc-500" />
          <input
            v-model="searchQuery"
            type="text"
            placeholder="Filter by hostname or IP..."
            class="w-full sm:w-52 pl-8 pr-3 py-1.5 rounded-lg bg-zinc-900 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-cyan-500/70"
          />
        </div>

        <!-- Role tabs -->
        <div class="flex items-center rounded-lg bg-zinc-900 border border-zinc-800 p-0.5 text-xs font-medium">
          <button
            @click="roleFilter = 'all'"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer',
              roleFilter === 'all'
                ? 'bg-zinc-800 text-white shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            {{ t('services_filter_all') }}
          </button>
          <button
            @click="roleFilter = 'controlplane'"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer flex items-center gap-1',
              roleFilter === 'controlplane'
                ? 'bg-zinc-800 text-violet-300 shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            <Shield class="w-3 h-3" />
            <span>CP</span>
          </button>
          <button
            @click="roleFilter = 'worker'"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer flex items-center gap-1',
              roleFilter === 'worker'
                ? 'bg-zinc-800 text-sky-300 shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            <Cpu class="w-3 h-3" />
            <span>{{ t('stat_workers') }}</span>
          </button>
        </div>

        <!-- Add Worker Button in Header Bar -->
        <button
          @click="isAddModalOpen = true"
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-zinc-900 hover:bg-zinc-800 border border-cyan-500/40 text-cyan-300 hover:text-cyan-200 text-xs font-semibold transition-all cursor-pointer active:scale-95 shadow-sm"
          :title="t('add_worker_title')"
        >
          <Plus class="w-3.5 h-3.5 text-cyan-400" />
          <span>{{ t('proxmox_add_worker_btn') }}</span>
        </button>
      </div>
    </div>

    <!-- Node Cards Grid -->
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4 lg:gap-5">
      <NodeCard
        v-for="node in filteredNodes"
        :key="node.ip"
        :node="node"
        @open-services="emit('open-services', $event)"
        @open-logs="emit('open-logs', $event)"
        @open-reboot="emit('open-reboot', $event)"
      />
    </div>

    <!-- Empty state when search matches nothing -->
    <div
      v-if="filteredNodes.length === 0"
      class="py-16 text-center rounded-2xl bg-zinc-900/40 border border-zinc-800/60"
    >
      <Server class="w-10 h-10 mx-auto text-zinc-600 mb-3" />
      <p class="text-sm font-semibold text-zinc-300">{{ t('nodes_empty_title') }}</p>
      <p class="text-xs text-zinc-500 mt-1">{{ t('nodes_empty_hint') }}</p>
    </div>

    <!-- Add Worker Modal (Scale-Out Wizard) -->
    <AddWorkerModal
      :open="isAddModalOpen"
      @close="isAddModalOpen = false"
      @success="handleWorkerCreated"
      @error="handleWorkerError"
    />
  </div>
</template>

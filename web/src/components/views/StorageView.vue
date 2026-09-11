<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import {
  HardDrive,
  Database,
  FolderTree,
  CheckCircle2,
  Activity,
  Server,
  RefreshCw,
  Thermometer,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type { NodeOverview, NodeDisksOverview } from '../../types'
import { fetchAllNodeDisks } from '../../api'

const props = defineProps<{
  nodes: NodeOverview[]
}>()

const loading = ref(false)
const nodeDisks = ref<NodeDisksOverview[]>([])
const selectedNodeIP = ref<string>('all')

const loadDisks = async () => {
  if (props.nodes.length === 0) return
  loading.value = true
  try {
    nodeDisks.value = await fetchAllNodeDisks(props.nodes)
  } catch (err) {
    console.error('Failed to load disks:', err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadDisks()
})

watch(
  () => props.nodes,
  () => {
    if (nodeDisks.value.length === 0 && props.nodes.length > 0) {
      loadDisks()
    }
  },
  { deep: true },
)

// Summary metrics computed across cluster
const totalCapacity = computed(() => {
  let totalGB = 0
  for (const nd of nodeDisks.value) {
    for (const d of nd.disks) {
      totalGB += parseFloat(d.size) || 0
    }
  }
  return `${totalGB.toFixed(0)} GB`
})

const totalUsed = computed(() => {
  let usedGB = 0
  for (const nd of nodeDisks.value) {
    for (const d of nd.disks) {
      for (const p of d.partitions) {
        if (p.used) {
          const val = parseFloat(p.used)
          if (p.used.includes('GB')) usedGB += val
          else if (p.used.includes('MB')) usedGB += val / 1024
        }
      }
    }
  }
  return `${usedGB.toFixed(1)} GB`
})

const totalDisksCount = computed(() => {
  return nodeDisks.value.reduce((acc, nd) => acc + nd.disks.length, 0)
})

const totalPartitionsCount = computed(() => {
  let count = 0
  for (const nd of nodeDisks.value) {
    for (const d of nd.disks) {
      count += d.partitions.length
    }
  }
  return count
})

const filteredNodeDisks = computed(() => {
  if (selectedNodeIP.value === 'all') {
    return nodeDisks.value
  }
  return nodeDisks.value.filter((nd) => nd.nodeIP === selectedNodeIP.value)
})
</script>

<template>
  <div class="space-y-6">
    <!-- Header & Controls -->
    <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
      <div>
        <div class="flex items-center gap-2.5">
          <h2 class="text-xl font-bold tracking-tight text-zinc-100 flex items-center gap-2">
            <HardDrive class="w-5 h-5 text-cyan-400" />
            <span>{{ t('storage_title') }}</span>
          </h2>
          <span class="px-2 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-800 text-cyan-300 border border-zinc-700/60">
            {{ totalDisksCount }} drives
          </span>
        </div>
        <p class="text-xs text-zinc-400 mt-1">
          {{ t('storage_subtitle') }}
        </p>
      </div>

      <!-- Actions & Node Filter -->
      <div class="flex items-center gap-2.5 self-stretch sm:self-auto">
        <!-- Node selector filter -->
        <div class="flex items-center gap-1.5 bg-zinc-900 border border-zinc-800 rounded-xl px-3 py-1.5 text-xs text-zinc-300">
          <Server class="w-3.5 h-3.5 text-zinc-400" />
          <span class="text-zinc-500 hidden sm:inline">{{ t('storage_filter_node') }}:</span>
          <select
            v-model="selectedNodeIP"
            class="bg-transparent text-zinc-200 text-xs font-semibold focus:outline-none cursor-pointer pr-1"
          >
            <option value="all" class="bg-zinc-900 text-zinc-200">
              {{ t('storage_all_nodes') }}
            </option>
            <option
              v-for="node in nodes"
              :key="node.ip"
              :value="node.ip"
              class="bg-zinc-900 text-zinc-200"
            >
              {{ node.hostname }} ({{ node.ip }})
            </option>
          </select>
        </div>

        <!-- Reload button -->
        <button
          @click="loadDisks"
          :disabled="loading"
          class="flex items-center gap-1.5 px-3 py-2 rounded-xl bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
          :title="t('refresh')"
        >
          <RefreshCw :class="['w-3.5 h-3.5 text-cyan-400', loading ? 'animate-spin' : '']" />
          <span class="hidden sm:inline">{{ loading ? t('refreshing') : t('refresh') }}</span>
        </button>
      </div>
    </div>

    <!-- Storage Summary Metrics Grid -->
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 md:gap-4">
      <!-- Total Storage -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-cyan-950/60 text-cyan-400 border border-cyan-800/50">
          <Database class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('storage_stat_total') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-zinc-100 font-mono">{{ totalCapacity }}</span>
          </div>
        </div>
      </div>

      <!-- Used Storage -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-teal-950/60 text-teal-400 border border-teal-800/50">
          <Activity class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('storage_stat_used') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-teal-300 font-mono">{{ totalUsed }}</span>
          </div>
        </div>
      </div>

      <!-- Physical Disks -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-violet-950/60 text-violet-400 border border-violet-800/50">
          <HardDrive class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('storage_stat_disks') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-violet-300 font-mono">{{ totalDisksCount }}</span>
            <span class="text-[11px] text-zinc-500 font-normal">drives</span>
          </div>
        </div>
      </div>

      <!-- Filesystem Partitions -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-emerald-950/60 text-emerald-400 border border-emerald-800/50">
          <FolderTree class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('storage_stat_partitions') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-emerald-400 font-mono">{{ totalPartitionsCount }}</span>
            <span class="text-[11px] text-emerald-500/80 font-medium">mounts</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Node Disks List -->
    <div class="space-y-6">
      <div
        v-for="nd in filteredNodeDisks"
        :key="nd.nodeIP"
        class="bg-zinc-900/80 border border-zinc-800/90 rounded-2xl p-5 backdrop-blur-sm space-y-4 shadow-md"
      >
        <!-- Node Header -->
        <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2 pb-3 border-b border-zinc-800/70">
          <div class="flex items-center gap-3">
            <div class="p-2 rounded-xl bg-zinc-800/80 text-cyan-400 border border-zinc-700/60">
              <Server class="w-4 h-4" />
            </div>
            <div>
              <div class="flex items-center gap-2">
                <h3 class="text-base font-bold text-zinc-100 font-mono">{{ nd.hostname }}</h3>
                <span class="text-xs font-mono text-zinc-400 bg-zinc-950 px-2 py-0.5 rounded border border-zinc-800">
                  {{ nd.nodeIP }}
                </span>
              </div>
              <p class="text-xs text-zinc-400 mt-0.5">
                {{ nd.disks.length }} physical drives connected
              </p>
            </div>
          </div>

          <!-- Total space for node -->
          <div class="flex items-center gap-3">
            <div class="text-right">
              <span class="text-xs text-zinc-400 block">{{ t('storage_stat_used') }}:</span>
              <span class="text-xs font-mono font-semibold text-cyan-300">{{ nd.usedStorage }} / {{ nd.totalStorage }}</span>
            </div>
            <div class="w-24 bg-zinc-950 rounded-full h-2 overflow-hidden border border-zinc-800">
              <div
                class="bg-gradient-to-r from-cyan-500 to-teal-400 h-2 rounded-full transition-all duration-500"
                :style="{ width: `${nd.usedPercent}%` }"
              ></div>
            </div>
          </div>
        </div>

        <!-- Disks Grid for this Node -->
        <div class="grid grid-cols-1 gap-4">
          <div
            v-for="disk in nd.disks"
            :key="disk.name"
            class="bg-zinc-950/70 rounded-xl border border-zinc-800/80 p-4 space-y-3.5 hover:border-zinc-700/80 transition-colors"
          >
            <!-- Disk Device Header -->
            <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
              <div class="flex items-center gap-3">
                <div class="p-2 rounded-lg bg-zinc-900 text-teal-400 border border-zinc-800">
                  <HardDrive class="w-4 h-4" />
                </div>
                <div>
                  <div class="flex items-center gap-2">
                    <span class="text-sm font-mono font-bold text-zinc-100">{{ disk.name }}</span>
                    <!-- Drive Type Badge -->
                    <span class="px-2 py-0.5 rounded text-[11px] font-semibold bg-zinc-900 text-sky-300 border border-sky-800/50">
                      {{ disk.type }}
                    </span>
                    <!-- Bus Badge -->
                    <span class="px-2 py-0.5 rounded text-[10px] font-mono text-zinc-400 bg-zinc-900 border border-zinc-800">
                      {{ disk.bus }}
                    </span>
                  </div>
                  <p v-if="disk.model" class="text-xs text-zinc-500 mt-0.5 font-mono">
                    {{ disk.model }} <span v-if="disk.serial">({{ disk.serial }})</span>
                  </p>
                </div>
              </div>

              <!-- Disk Health & Size Badges -->
              <div class="flex items-center gap-2">
                <div v-if="disk.temp" class="flex items-center gap-1 text-xs text-zinc-400 bg-zinc-900 px-2.5 py-1 rounded-lg border border-zinc-800">
                  <Thermometer class="w-3.5 h-3.5 text-amber-400" />
                  <span class="font-mono text-zinc-300">{{ disk.temp }}</span>
                </div>

                <div class="text-xs font-mono font-bold px-2.5 py-1 rounded-lg bg-zinc-900 text-zinc-200 border border-zinc-800">
                  {{ disk.size }}
                </div>

                <div
                  :class="[
                    'inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-semibold border',
                    disk.healthy
                      ? 'bg-emerald-950/60 text-emerald-400 border-emerald-800/60 shadow-[0_0_10px_rgba(16,185,129,0.25)]'
                      : 'bg-rose-950/60 text-rose-400 border-rose-800/60',
                  ]"
                >
                  <CheckCircle2 class="w-3.5 h-3.5" />
                  <span>{{ disk.healthy ? 'Healthy' : 'Alert' }}</span>
                </div>
              </div>
            </div>

            <!-- Partitions Table -->
            <div class="overflow-x-auto rounded-lg border border-zinc-800/70 bg-zinc-900/50">
              <table class="w-full text-left text-xs">
                <thead>
                  <tr class="border-b border-zinc-800 text-[11px] text-zinc-400 uppercase tracking-wider bg-zinc-900/90">
                    <th class="py-2.5 px-3 font-semibold">{{ t('storage_part_device') }}</th>
                    <th class="py-2.5 px-3 font-semibold">{{ t('storage_part_label') }}</th>
                    <th class="py-2.5 px-3 font-semibold">{{ t('storage_part_fs') }}</th>
                    <th class="py-2.5 px-3 font-semibold">{{ t('storage_part_mount') }}</th>
                    <th class="py-2.5 px-3 font-semibold">{{ t('storage_disk_size') }}</th>
                    <th class="py-2.5 px-3 font-semibold">{{ t('storage_part_used') }}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-zinc-800/50 font-mono text-zinc-300">
                  <tr
                    v-for="part in disk.partitions"
                    :key="part.device"
                    class="hover:bg-zinc-800/40 transition-colors"
                  >
                    <!-- Device -->
                    <td class="py-2.5 px-3 font-bold text-cyan-300">
                      {{ part.device }}
                    </td>
                    <!-- Label -->
                    <td class="py-2.5 px-3 text-zinc-400 font-sans text-xs">
                      {{ part.type || part.label || '-' }}
                    </td>
                    <!-- FS -->
                    <td class="py-2.5 px-3">
                      <span
                        v-if="part.filesystem && part.filesystem !== 'none'"
                        class="px-1.5 py-0.5 rounded bg-zinc-800 text-zinc-300 text-[10px] border border-zinc-700/60"
                      >
                        {{ part.filesystem }}
                      </span>
                      <span v-else class="text-zinc-600">-</span>
                    </td>
                    <!-- Mountpoint -->
                    <td class="py-2.5 px-3">
                      <span
                        v-if="part.mountpoint && part.mountpoint !== '-'"
                        class="text-emerald-400 font-semibold"
                      >
                        {{ part.mountpoint }}
                      </span>
                      <span v-else class="text-zinc-600">-</span>
                    </td>
                    <!-- Size -->
                    <td class="py-2.5 px-3 text-zinc-300">
                      {{ part.size }}
                    </td>
                    <!-- Used -->
                    <td class="py-2.5 px-3">
                      <div v-if="part.used" class="flex items-center gap-2">
                        <span class="text-xs text-zinc-300">{{ part.used }}</span>
                        <span v-if="part.usedPercent !== undefined" class="text-[10px] text-zinc-500">
                          ({{ part.usedPercent }}%)
                        </span>
                      </div>
                      <span v-else class="text-zinc-600">-</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </div>

      <!-- Empty state if no disks -->
      <div
        v-if="filteredNodeDisks.length === 0 && !loading"
        class="py-16 text-center rounded-2xl bg-zinc-900/40 border border-zinc-800/60"
      >
        <HardDrive class="w-10 h-10 mx-auto text-zinc-600 mb-3" />
        <p class="text-sm font-semibold text-zinc-300">{{ t('storage_no_disks') }}</p>
      </div>
    </div>
  </div>
</template>

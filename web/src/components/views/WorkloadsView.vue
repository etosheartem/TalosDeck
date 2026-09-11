<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Boxes,
  Search,
  RefreshCw,
  CheckCircle2,
  AlertTriangle,
  RotateCcw,
  Copy,
  Check,
  Server,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type { K8sPod } from '../../types'
import { fetchK8sPods } from '../../api'

const pods = ref<K8sPod[]>([])
const loading = ref(false)
const searchQuery = ref('')
const selectedNamespace = ref<string>('all')
const selectedStatus = ref<'all' | 'Running' | 'issues'>('all')
const copiedPodId = ref<string | null>(null)

const loadPods = async () => {
  loading.value = true
  try {
    pods.value = await fetchK8sPods()
  } catch (err) {
    console.error('Failed to fetch workloads:', err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadPods()
})

// Unique namespaces with counts
const namespaces = computed(() => {
  const map: Record<string, number> = {}
  for (const p of pods.value) {
    map[p.namespace] = (map[p.namespace] || 0) + 1
  }
  return Object.entries(map).map(([name, count]) => ({ name, count }))
})

// Filtered pods
const filteredPods = computed(() => {
  return pods.value.filter((pod) => {
    // Namespace filter
    if (selectedNamespace.value !== 'all' && pod.namespace !== selectedNamespace.value) {
      return false
    }

    // Status filter
    if (selectedStatus.value === 'Running' && pod.status !== 'Running') {
      return false
    }
    if (selectedStatus.value === 'issues' && pod.status === 'Running') {
      return false
    }

    // Search query
    const q = searchQuery.value.trim().toLowerCase()
    if (q) {
      const matchName = pod.name.toLowerCase().includes(q)
      const matchNode = pod.nodeName.toLowerCase().includes(q)
      const matchIP = pod.ip.includes(q)
      const matchNS = pod.namespace.toLowerCase().includes(q)
      if (!matchName && !matchNode && !matchIP && !matchNS) return false
    }

    return true
  })
})

// Summary metrics
const totalPodsCount = computed(() => pods.value.length)
const runningPodsCount = computed(() => pods.value.filter((p) => p.status === 'Running').length)
const issuePodsCount = computed(() => pods.value.filter((p) => p.status !== 'Running').length)
const totalRestartsCount = computed(() => pods.value.reduce((acc, p) => acc + p.restarts, 0))

const copyPodName = async (name: string, id: string) => {
  try {
    await navigator.clipboard.writeText(name)
    copiedPodId.value = id
    setTimeout(() => {
      copiedPodId.value = null
    }, 2000)
  } catch (err) {
    console.error('Failed to copy', err)
  }
}
</script>

<template>
  <div class="space-y-6">
    <!-- Header & Controls -->
    <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
      <div>
        <div class="flex items-center gap-2.5">
          <h2 class="text-xl font-bold tracking-tight text-zinc-100 flex items-center gap-2">
            <Boxes class="w-5 h-5 text-cyan-400" />
            <span>{{ t('workloads_title') }}</span>
          </h2>
          <span class="px-2 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-800 text-cyan-300 border border-zinc-700/60">
            {{ totalPodsCount }} pods
          </span>
        </div>
        <p class="text-xs text-zinc-400 mt-1">
          {{ t('workloads_subtitle') }}
        </p>
      </div>

      <div class="flex items-center gap-2.5 self-stretch sm:self-auto">
        <button
          @click="loadPods"
          :disabled="loading"
          class="flex items-center gap-1.5 px-3 py-2 rounded-xl bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
          :title="t('refresh')"
        >
          <RefreshCw :class="['w-3.5 h-3.5 text-cyan-400', loading ? 'animate-spin' : '']" />
          <span class="hidden sm:inline">{{ loading ? t('refreshing') : t('refresh') }}</span>
        </button>
      </div>
    </div>

    <!-- Metrics Cards Grid -->
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 md:gap-4">
      <!-- Total Pods -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-cyan-950/60 text-cyan-400 border border-cyan-800/50">
          <Boxes class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('workloads_stat_total') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-zinc-100 font-mono">{{ totalPodsCount }}</span>
          </div>
        </div>
      </div>

      <!-- Running Pods -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-emerald-950/60 text-emerald-400 border border-emerald-800/50">
          <CheckCircle2 class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('workloads_stat_running') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-emerald-400 font-mono">{{ runningPodsCount }}</span>
          </div>
        </div>
      </div>

      <!-- Issues / Failed -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-amber-950/60 text-amber-400 border border-amber-800/50">
          <AlertTriangle class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('workloads_stat_issues') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-amber-300 font-mono">{{ issuePodsCount }}</span>
          </div>
        </div>
      </div>

      <!-- Total Restarts -->
      <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-2xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
        <div class="p-3 rounded-xl bg-violet-950/60 text-violet-400 border border-violet-800/50">
          <RotateCcw class="w-5 h-5" />
        </div>
        <div>
          <p class="text-xs font-medium text-zinc-400">{{ t('workloads_stat_restarts') }}</p>
          <div class="flex items-baseline gap-1 mt-0.5">
            <span class="text-2xl font-bold tracking-tight text-violet-300 font-mono">{{ totalRestartsCount }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Filter Bar: Search + Namespace Pills + Status Toggle -->
    <div class="flex flex-col lg:flex-row items-stretch lg:items-center justify-between gap-3 p-3 bg-zinc-900/80 border border-zinc-800/80 rounded-2xl backdrop-blur-sm">
      <!-- Namespace Chips -->
      <div class="flex items-center gap-1.5 overflow-x-auto pb-1 lg:pb-0">
        <button
          @click="selectedNamespace = 'all'"
          :class="[
            'px-3 py-1.5 rounded-xl text-xs font-semibold transition-all cursor-pointer whitespace-nowrap border',
            selectedNamespace === 'all'
              ? 'bg-zinc-800 text-cyan-300 border-cyan-500/50 shadow-md shadow-cyan-950/30'
              : 'bg-zinc-950/60 text-zinc-400 hover:text-zinc-200 border-zinc-800/80 hover:border-zinc-700',
          ]"
        >
          {{ t('workloads_namespace_all') }} ({{ totalPodsCount }})
        </button>
        <button
          v-for="ns in namespaces"
          :key="ns.name"
          @click="selectedNamespace = ns.name"
          :class="[
            'px-2.5 py-1.5 rounded-xl text-xs font-mono font-medium transition-all cursor-pointer whitespace-nowrap border',
            selectedNamespace === ns.name
              ? 'bg-zinc-800 text-cyan-300 border-cyan-500/50 shadow-md shadow-cyan-950/30'
              : 'bg-zinc-950/60 text-zinc-400 hover:text-zinc-200 border-zinc-800/80 hover:border-zinc-700',
          ]"
        >
          <span>{{ ns.name }}</span>
          <span class="ml-1.5 text-[10px] text-zinc-500 font-sans">({{ ns.count }})</span>
        </button>
      </div>

      <!-- Search & Status Filter -->
      <div class="flex items-center gap-2">
        <!-- Search Input -->
        <div class="relative w-full sm:w-56">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-zinc-500" />
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="t('workloads_search')"
            class="w-full pl-8 pr-3 py-1.5 rounded-xl bg-zinc-950 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-cyan-500/70"
          />
        </div>

        <!-- Status Filter -->
        <div class="flex items-center rounded-xl bg-zinc-950 border border-zinc-800 p-0.5 text-xs font-medium shrink-0">
          <button
            @click="selectedStatus = 'all'"
            :class="[
              'px-2.5 py-1 rounded-lg transition-all cursor-pointer',
              selectedStatus === 'all'
                ? 'bg-zinc-800 text-white shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            {{ t('services_filter_all') }}
          </button>
          <button
            @click="selectedStatus = 'Running'"
            :class="[
              'px-2.5 py-1 rounded-lg transition-all cursor-pointer text-emerald-400',
              selectedStatus === 'Running'
                ? 'bg-zinc-800 shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            Running
          </button>
          <button
            @click="selectedStatus = 'issues'"
            :class="[
              'px-2.5 py-1 rounded-lg transition-all cursor-pointer text-amber-400',
              selectedStatus === 'issues'
                ? 'bg-zinc-800 shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            Issues
          </button>
        </div>
      </div>
    </div>

    <!-- Pods Table Container -->
    <div class="rounded-2xl border border-zinc-800/90 bg-zinc-900/80 shadow-md overflow-hidden backdrop-blur-sm">
      <div class="overflow-x-auto">
        <table class="w-full text-left text-xs">
          <thead>
            <tr class="border-b border-zinc-800 text-[11px] text-zinc-400 uppercase tracking-wider bg-zinc-900/90">
              <th class="py-3 px-4 font-semibold">{{ t('workloads_col_pod') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_status') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_ready') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_node') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_restarts') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_ip') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_resources') }}</th>
              <th class="py-3 px-3 font-semibold">{{ t('workloads_col_age') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-800/50">
            <tr
              v-for="pod in filteredPods"
              :key="pod.id"
              class="hover:bg-zinc-800/30 transition-colors group"
            >
              <!-- Pod Name & Namespace -->
              <td class="py-3 px-4">
                <div class="flex items-center gap-2">
                  <div>
                    <div class="flex items-center gap-1.5">
                      <span class="font-mono font-bold text-zinc-100 group-hover:text-cyan-300 transition-colors">
                        {{ pod.name }}
                      </span>
                      <button
                        @click="copyPodName(pod.name, pod.id)"
                        class="p-0.5 text-zinc-600 hover:text-cyan-400 rounded transition-colors cursor-pointer"
                        :title="t('node_copy_ip')"
                      >
                        <Check v-if="copiedPodId === pod.id" class="w-3 h-3 text-emerald-400" />
                        <Copy v-else class="w-3 h-3 opacity-0 group-hover:opacity-100 transition-opacity" />
                      </button>
                    </div>
                    <span class="inline-block mt-0.5 px-2 py-0.2 rounded-md bg-zinc-950 text-zinc-400 border border-zinc-800 text-[10px] font-mono">
                      {{ pod.namespace }}
                    </span>
                  </div>
                </div>
              </td>

              <!-- Status Badge with Glowing Dot -->
              <td class="py-3 px-3">
                <span
                  :class="[
                    'inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[11px] font-semibold tracking-wide border',
                    pod.status === 'Running'
                      ? 'bg-emerald-950/60 text-emerald-300 border-emerald-800/60 shadow-[0_0_10px_rgba(16,185,129,0.25)]'
                      : 'bg-amber-950/60 text-amber-300 border-amber-800/60 shadow-[0_0_10px_rgba(245,158,11,0.25)]',
                  ]"
                >
                  <span
                    :class="[
                      'w-1.5 h-1.5 rounded-full',
                      pod.status === 'Running' ? 'bg-emerald-400' : 'bg-amber-400',
                    ]"
                  ></span>
                  <span>{{ pod.status }}</span>
                </span>
              </td>

              <!-- Containers Ready -->
              <td class="py-3 px-3 font-mono font-medium text-zinc-300">
                {{ pod.readyContainers }}
              </td>

              <!-- Node Placement -->
              <td class="py-3 px-3">
                <div class="flex items-center gap-1.5 font-mono text-zinc-300">
                  <Server class="w-3 h-3 text-zinc-500" />
                  <span>{{ pod.nodeName }}</span>
                </div>
              </td>

              <!-- Restarts -->
              <td class="py-3 px-3 font-mono">
                <span
                  :class="[
                    'px-2 py-0.5 rounded text-xs font-semibold',
                    pod.restarts > 0
                      ? 'bg-amber-950/80 text-amber-300 border border-amber-800/60'
                      : 'text-zinc-500',
                  ]"
                >
                  {{ pod.restarts }}
                </span>
              </td>

              <!-- IP -->
              <td class="py-3 px-3 font-mono text-zinc-400">
                {{ pod.ip }}
              </td>

              <!-- CPU / RAM -->
              <td class="py-3 px-3 font-mono text-[11px] text-zinc-400">
                <span v-if="pod.cpu && pod.memory">
                  {{ pod.cpu }} / {{ pod.memory }}
                </span>
                <span v-else class="text-zinc-600">-</span>
              </td>

              <!-- Age -->
              <td class="py-3 px-3 font-mono text-zinc-500 text-[11px]">
                {{ pod.age }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Empty state -->
      <div
        v-if="filteredPods.length === 0 && !loading"
        class="py-16 text-center"
      >
        <Boxes class="w-10 h-10 mx-auto text-zinc-600 mb-3" />
        <p class="text-sm font-semibold text-zinc-300">{{ t('workloads_empty') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Boxes,
  Search,
  RefreshCw,
  Copy,
  Check,
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

    // Search query (FE-01: Null-safe search matching across nodeName/node and ip/podIp)
    const q = searchQuery.value.trim().toLowerCase()
    if (q) {
      const matchName = (pod.name || '').toLowerCase().includes(q)
      const matchNode = (pod.nodeName || pod.node || '').toLowerCase().includes(q)
      const matchIP = (pod.ip || pod.podIp || '').includes(q)
      const matchNS = (pod.namespace || '').toLowerCase().includes(q)
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
  <div class="space-y-4">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <div class="flex items-baseline gap-2">
          <h2 class="text-sm font-semibold">{{ t('workloads_title') }}</h2>
          <span class="mono text-[10px]" style="color: var(--text-faint)">{{ filteredPods.length }} / {{ totalPodsCount }}</span>
        </div>
        <p class="mt-1 text-[11px]" style="color: var(--text-muted)">{{ t('workloads_subtitle') }}</p>
      </div>
      <button class="control flex h-8 items-center gap-1.5 px-2.5 text-xs" :disabled="loading" @click="loadPods">
        <RefreshCw :class="['h-3.5 w-3.5', { 'animate-spin': loading }]" />{{ loading ? t('refreshing') : t('refresh') }}
      </button>
    </div>

    <section class="panel overflow-hidden">
      <div class="grid grid-cols-2 divide-x divide-y lg:grid-cols-4 lg:divide-y-0" style="border-color: var(--border)">
        <div v-for="item in [
          [t('workloads_stat_total'), totalPodsCount, ''],
          [t('workloads_stat_running'), runningPodsCount, 'ok'],
          [t('workloads_stat_issues'), issuePodsCount, issuePodsCount ? 'warning' : ''],
          [t('workloads_stat_restarts'), totalRestartsCount, totalRestartsCount ? 'warning' : ''],
        ]" :key="String(item[0])" class="metric">
          <span :class="['status-dot', item[2] === 'ok' ? 'is-ok' : item[2] === 'warning' ? 'is-warning' : '']" />
          <div><div class="metric-label">{{ item[0] }}</div><div class="mono metric-value">{{ item[1] }}</div></div>
        </div>
      </div>
    </section>

    <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
      <label class="control relative flex flex-1 items-center sm:max-w-xs">
        <Search class="absolute left-2.5 h-3.5 w-3.5" style="color: var(--text-faint)" />
        <input v-model="searchQuery" class="h-8 w-full bg-transparent pl-8 pr-2 text-xs outline-none" :placeholder="t('workloads_search_placeholder')" />
      </label>
      <select v-model="selectedNamespace" class="control h-8 px-2 text-xs">
        <option value="all">{{ t('workloads_all_namespaces') }}</option>
        <option v-for="ns in namespaces" :key="ns.name" :value="ns.name">{{ ns.name }} ({{ ns.count }})</option>
      </select>
      <div class="filter-group">
        <button :class="{ active: selectedStatus === 'all' }" @click="selectedStatus = 'all'">{{ t('services_filter_all') }}</button>
        <button :class="{ active: selectedStatus === 'Running' }" @click="selectedStatus = 'Running'">Running</button>
        <button :class="{ active: selectedStatus === 'issues' }" @click="selectedStatus = 'issues'">Issues</button>
      </div>
    </div>

    <div class="panel overflow-x-auto">
      <table class="data-table">
        <thead><tr>
          <th>{{ t('workloads_col_pod') }}</th><th>{{ t('workloads_col_status') }}</th><th>{{ t('workloads_col_ready') }}</th>
          <th>{{ t('workloads_col_node') }}</th><th>{{ t('workloads_col_restarts') }}</th><th>{{ t('workloads_col_ip') }}</th>
          <th>{{ t('workloads_col_resources') }}</th><th>{{ t('workloads_col_age') }}</th>
        </tr></thead>
        <tbody>
          <tr v-for="pod in filteredPods" :key="pod.id">
            <td><div class="flex items-center gap-1.5"><div><div class="mono font-semibold" style="color: var(--text)">{{ pod.name }}</div><div class="mono secondary">{{ pod.namespace }}</div></div><button class="copy" @click="copyPodName(pod.name, pod.id)"><Check v-if="copiedPodId === pod.id" class="h-3 w-3"/><Copy v-else class="h-3 w-3"/></button></div></td>
            <td><span :class="['status-chip', pod.status === 'Running' ? 'is-ok' : 'is-danger']"><span :class="['status-dot', pod.status === 'Running' ? 'is-ok' : 'is-warning']"/>{{ pod.status }}</span></td>
            <td class="mono">{{ pod.readyContainers }}</td>
            <td class="mono">{{ pod.nodeName || pod.node || '—' }}</td>
            <td class="mono" :style="pod.restarts ? 'color: var(--warning)' : ''">{{ pod.restarts }}</td>
            <td class="mono">{{ pod.ip || pod.podIp || '—' }}</td>
            <td class="mono">{{ pod.cpu && pod.memory ? `${pod.cpu} / ${pod.memory}` : '—' }}</td>
            <td class="mono">{{ pod.age }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!filteredPods.length && !loading" class="empty"><Boxes class="h-5 w-5"/>{{ t('workloads_empty') }}</div>
    </div>
  </div>
</template>

<style scoped>
.metric{display:flex;min-height:66px;align-items:center;gap:10px;padding:10px 14px;border-color:var(--border)}
.metric-label{color:var(--text-muted);font-size:10px}.metric-value{margin-top:2px;color:var(--text);font-size:15px;font-weight:650}
.filter-group{display:flex;height:32px;gap:2px;border:1px solid var(--border);border-radius:6px;padding:2px;background:var(--surface-raised)}
.filter-group button{border-radius:4px;padding:0 9px;color:var(--text-muted);font-size:10px}.filter-group button.active{background:var(--border);color:var(--text)}
.data-table{width:100%;min-width:940px;border-collapse:collapse}.data-table th{height:34px;padding:0 12px;border-bottom:1px solid var(--border);color:var(--text-faint);font-size:9px;font-weight:650;letter-spacing:.06em;text-align:left;text-transform:uppercase}
.data-table td{height:56px;padding:8px 12px;border-bottom:1px solid var(--border);color:var(--text-muted);font-size:10px}.data-table tr:last-child td{border-bottom:0}.data-table tbody tr:hover{background:var(--surface-hover)}
.secondary{margin-top:3px;color:var(--text-faint);font-size:9px}.copy{color:var(--text-faint)}.copy:hover{color:var(--accent)}.empty{display:flex;min-height:120px;align-items:center;justify-content:center;gap:8px;color:var(--text-muted);font-size:11px}
</style>

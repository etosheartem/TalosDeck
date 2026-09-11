<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import {
  HardDrive,
  Server,
  RefreshCw,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type { NodeOverview, NodeDisksOverview } from '../../types'
import { fetchAllNodeDisks } from '../../api'
import { sizeToGiB } from '../../utils/storage'

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
      totalGB += sizeToGiB(d.sizeBytes ?? d.size)
    }
  }
  return `${totalGB.toFixed(0)} GB`
})

const totalUsed = computed(() => {
  let usedGB = 0
  for (const nd of nodeDisks.value) {
    for (const d of nd.disks) {
      for (const p of d.partitions) {
		if (p.usedBytes !== undefined || p.used) {
		  usedGB += sizeToGiB(p.usedBytes ?? p.used ?? 0)
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
<div class="space-y-4">
  <div class="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between"><div><div class="flex items-baseline gap-2"><h2 class="text-sm font-semibold">{{ t('storage_title') }}</h2><span class="mono text-[10px] text-zinc-500">{{ totalDisksCount }} {{ t('storage_drives_suffix') }}</span></div><p class="mt-1 text-[11px] text-zinc-400">{{ t('storage_subtitle') }}</p></div><div class="flex gap-2"><label class="control flex items-center gap-2 px-2"><Server class="h-3.5 w-3.5 text-zinc-500"/><select v-model="selectedNodeIP" class="bg-transparent text-xs outline-none"><option value="all">{{ t('storage_all_nodes') }}</option><option v-for="node in nodes" :key="node.ip" :value="node.ip">{{ node.hostname }} · {{ node.ip }}</option></select></label><button class="control flex items-center gap-1.5 px-2.5 text-xs" :disabled="loading" @click="loadDisks"><RefreshCw :class="['h-3.5 w-3.5',{'animate-spin':loading}]"/>{{ loading?t('refreshing'):t('refresh') }}</button></div></div>
  <section class="panel overflow-hidden"><div class="grid grid-cols-2 divide-x divide-y lg:grid-cols-4 lg:divide-y-0"><div v-for="item in [[t('storage_stat_total'),totalCapacity],[t('storage_stat_used'),totalUsed],[t('storage_stat_disks'),totalDisksCount],[t('storage_stat_partitions'),totalPartitionsCount]]" :key="String(item[0])" class="metric"><div class="text-[10px] text-zinc-400">{{ item[0] }}</div><div class="mono mt-1 text-sm font-semibold">{{ item[1] }}</div></div></div></section>
  <section v-for="nd in filteredNodeDisks" :key="nd.nodeIP" class="panel overflow-hidden"><header class="flex min-h-14 items-center justify-between gap-3 border-b border-zinc-800 px-3 py-2"><div><div class="mono text-xs font-semibold">{{ nd.hostname }}</div><div class="mono mt-1 text-[9px] text-zinc-500">{{ nd.nodeIP }} · {{ nd.disks.length }} drives</div></div><div class="mono text-[10px] text-zinc-400">{{ nd.usedStorage }} / {{ nd.totalStorage }} · {{ nd.usedPercent }}%</div></header><div class="overflow-x-auto"><table class="data-table"><thead><tr><th>{{ t('storage_part_device') }}</th><th>Model</th><th>Type / Bus</th><th>{{ t('storage_disk_size') }}</th><th>Health</th><th>{{ t('storage_stat_partitions') }}</th><th>{{ t('storage_part_mount') }}</th></tr></thead><tbody><tr v-for="disk in nd.disks" :key="disk.name"><td class="mono font-semibold text-zinc-100">{{ disk.name }}</td><td class="mono">{{ disk.model||'—' }}</td><td class="mono">{{ disk.type }} · {{ disk.bus }}</td><td class="mono">{{ disk.size }}</td><td><span :class="['status-chip',disk.healthy===true?'is-ok':disk.healthy===false?'is-danger':'']"><span :class="['status-dot',disk.healthy===true?'is-ok':disk.healthy===false?'is-danger':'']"/>{{ disk.healthy===true?'Healthy':disk.healthy===false?'Alert':'Unknown' }}</span><div v-if="disk.temp" class="mono mt-1 text-[9px] text-zinc-500">{{ disk.temp }}</div></td><td class="mono">{{ disk.partitions.length }}</td><td class="mono"><div v-for="part in disk.partitions" :key="part.device" class="min-h-4">{{ part.mountpoint&&part.mountpoint!=='-'?part.mountpoint:'—' }} <small class="text-zinc-500">{{ part.used||'' }}</small></div></td></tr></tbody></table></div></section>
  <div v-if="!filteredNodeDisks.length&&!loading" class="panel flex min-h-28 items-center justify-center gap-2 text-xs text-zinc-400"><HardDrive class="h-5 w-5"/>{{ t('storage_no_disks') }}</div>
</div>
</template>
<style scoped>.metric{min-height:66px;padding:12px 14px;border-color:var(--border)}.data-table{width:100%;min-width:900px;border-collapse:collapse}.data-table th{height:34px;padding:0 12px;border-bottom:1px solid var(--border);color:var(--text-faint);font-size:9px;font-weight:650;letter-spacing:.06em;text-align:left;text-transform:uppercase}.data-table td{padding:10px 12px;border-bottom:1px solid var(--border);color:var(--text-muted);font-size:10px;vertical-align:top}.data-table tr:last-child td{border-bottom:0}.data-table tbody tr:hover{background:var(--surface-hover)}</style>

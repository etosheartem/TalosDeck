<script setup lang="ts">
import { computed } from 'vue'
import { Activity, Box, Boxes, GitBranch } from 'lucide-vue-next'
import { t, currentLocale } from '../i18n'
import type { ClusterInfo } from '../types'

const props = defineProps<{ cluster: ClusterInfo }>()
const readiness = computed(() => props.cluster.totalNodes ? Math.round(props.cluster.readyNodes / props.cluster.totalNodes * 100) : 0)
const cards = computed(() => [
  { label: t('stat_healthy'), value: `${props.cluster.readyNodes} / ${props.cluster.totalNodes}`, note: `${readiness.value}%`, icon: Activity, tone: readiness.value === 100 ? 'ok' : 'warn' },
  { label: t('stat_control_plane'), value: props.cluster.controlPlaneCount, note: currentLocale.value === 'ru' ? 'управляющих' : 'controllers', icon: GitBranch },
  { label: t('stat_workers'), value: props.cluster.workerCount, note: currentLocale.value === 'ru' ? 'рабочих' : 'compute', icon: Box },
  { label: currentLocale.value === 'ru' ? 'Всего ресурсов' : 'Fleet size', value: props.cluster.totalNodes, note: currentLocale.value === 'ru' ? 'машин' : 'machines', icon: Boxes },
])
</script>

<template>
  <section>
    <div class="overview-title">
      <div>
        <div class="flex items-center gap-2"><h2 class="text-xl font-semibold tracking-tight">{{ cluster.name }}</h2><span :class="['health-label',cluster.healthy?'ok':'bad']"><i/>{{ cluster.healthy?t('cluster_healthy'):t('cluster_degraded') }}</span></div>
        <div class="mono mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[11px]" style="color:var(--text-faint)"><span>{{ cluster.endpoint }}</span><span>Talos {{ cluster.talosVersion }}</span><span>Kubernetes {{ cluster.kubernetesVersion }}</span></div>
      </div>
    </div>
    <div class="mt-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
      <article v-for="card in cards" :key="card.label" class="panel metric-card">
        <div class="metric-top"><span>{{ card.label }}</span><component :is="card.icon" class="h-4 w-4"/></div>
        <div class="mt-4 flex items-end justify-between"><strong class="mono text-2xl font-medium">{{ card.value }}</strong><span :class="['metric-note',card.tone]">{{ card.note }}</span></div>
        <div v-if="card.tone" class="meter"><i :class="card.tone" :style="{width:`${readiness}%`}"/></div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.overview-title{display:flex;align-items:flex-end;justify-content:space-between;min-height:52px}.health-label{display:inline-flex;align-items:center;gap:6px;border-radius:10px;padding:3px 8px;font-size:11px;font-weight:600}.health-label i{width:6px;height:6px;border-radius:50%;background:currentColor}.health-label.ok{background:var(--success-muted);color:var(--success)}.health-label.bad{background:var(--danger-muted);color:var(--danger)}
.metric-card{min-height:112px;padding:14px 16px}.metric-top{display:flex;align-items:center;justify-content:space-between;color:var(--text-muted);font-size:12px}.metric-top svg{color:var(--text-faint)}.metric-note{color:var(--text-faint);font-size:11px}.metric-note.ok{color:var(--success)}.metric-note.warn{color:var(--warning)}.meter{height:3px;margin:14px -16px -14px;background:var(--border)}.meter i{display:block;height:100%}.meter i.ok{background:var(--success)}.meter i.warn{background:var(--warning)}
</style>

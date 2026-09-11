<script setup lang="ts">
import { computed } from 'vue'
import { CircleCheck, TriangleAlert } from 'lucide-vue-next'
import { currentLocale } from '../i18n'
import type { NodeOverview } from '../types'

const props = defineProps<{ nodes: NodeOverview[]; etcdHealthy: boolean }>()
const issues = computed(() => {
  const result = props.nodes.filter((node) => !node.ready).map((node) => ({
    key: node.ip,
    title: currentLocale.value === 'ru' ? `Нода ${node.hostname} не готова` : `Node ${node.hostname} is not ready`,
    detail: node.ip,
  }))
  if (!props.etcdHealthy) result.unshift({ key: 'etcd', title: currentLocale.value === 'ru' ? 'Кластер etcd деградирован' : 'etcd cluster is degraded', detail: 'etcd' })
  return result
})
</script>

<template>
  <section class="panel px-3 py-2.5" aria-live="polite">
    <div v-if="!issues.length" class="flex items-center gap-2 text-[11px]" style="color:var(--text-muted)">
      <CircleCheck class="h-3.5 w-3.5" style="color:var(--success)" />
      <span>{{ currentLocale === 'ru' ? 'Активных проблем нет' : 'No active issues' }}</span>
    </div>
    <div v-else class="space-y-2">
      <div class="flex items-center gap-2 text-[10px] font-semibold uppercase tracking-wide" style="color:var(--danger)"><TriangleAlert class="h-3.5 w-3.5"/>{{ currentLocale === 'ru' ? 'Требует внимания' : 'Needs attention' }} · {{ issues.length }}</div>
      <div class="flex flex-wrap gap-2"><div v-for="issue in issues" :key="issue.key" class="flex items-center gap-2 rounded-md border px-2 py-1.5 text-[10px]" style="border-color:#673139;background:var(--danger-muted)"><span class="status-dot is-danger"/><span>{{ issue.title }}</span><span class="mono" style="color:var(--text-faint)">{{ issue.detail }}</span></div></div>
    </div>
  </section>
</template>

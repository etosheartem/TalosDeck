<script setup lang="ts">
import { ref } from 'vue'
import { Copy, Check, ListChecks, Terminal, RotateCw } from 'lucide-vue-next'
import { t } from '../i18n'
import type { NodeOverview } from '../types'

const props = defineProps<{ node: NodeOverview }>()
const emit = defineEmits<{
  (e: 'open-services', node: NodeOverview): void
  (e: 'open-logs', node: NodeOverview): void
  (e: 'open-reboot', node: NodeOverview): void
}>()

const copied = ref(false)

const copyIp = async () => {
  try {
    await navigator.clipboard.writeText(props.node.ip)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch (error) {
    console.error('Failed to copy IP', error)
  }
}

const serviceState = (name: 'etcd' | 'kubelet' | 'containerd' | 'apid') => {
  const state = props.node.servicesSummary?.[name]
  if (state === 'Healthy') return 'ok'
  if (state === 'Degraded') return 'danger'
  return 'unknown'
}
</script>

<template>
  <tr :class="{ unavailable: !node.ready }">
    <td class="node-cell">
      <div class="flex items-center gap-2">
        <span :class="['status-dot', node.ready ? 'is-ok' : 'is-danger']" />
        <div class="min-w-0">
          <div class="mono truncate text-xs font-semibold" :title="node.hostname">{{ node.hostname }}</div>
          <button class="ip-button mono" :title="copied ? t('node_copied') : t('node_copy_ip')" @click="copyIp">
            {{ node.ip }}
            <Check v-if="copied" class="h-3 w-3" style="color: var(--success)" />
            <Copy v-else class="h-3 w-3" />
          </button>
        </div>
      </div>
    </td>
    <td>
      <span class="role-label">{{ node.role === 'controlplane' ? t('node_role_cp') : t('node_role_worker') }}</span>
    </td>
    <td>
      <span :class="['status-chip', node.ready ? 'is-ok' : 'is-danger']">
        {{ node.ready ? t('node_status_ready') : t('node_status_not_ready') }}
      </span>
    </td>
    <td class="mono metric-cell">
      <div>{{ node.version || '—' }}</div>
      <div class="secondary">K8s {{ node.kubernetesVersion || '—' }}</div>
    </td>
    <td class="mono metric-cell">
      <div>CPU {{ node.cpuUsage != null ? `${node.cpuUsage.toFixed(1)}%` : '—' }}</div>
      <div class="secondary">RAM {{ node.memoryUsage || '—' }}</div>
    </td>
    <td class="mono metric-cell">{{ node.uptime || '—' }}</td>
    <td>
      <div class="service-list mono">
        <span v-if="node.role === 'controlplane'" :title="`etcd: ${node.servicesSummary?.etcd || 'Unknown'}`"><i :class="serviceState('etcd')" />etcd</span>
        <span :title="`kubelet: ${node.servicesSummary?.kubelet || 'Unknown'}`"><i :class="serviceState('kubelet')" />kubelet</span>
        <span :title="`containerd: ${node.servicesSummary?.containerd || 'Unknown'}`"><i :class="serviceState('containerd')" />containerd</span>
        <span :title="`apid: ${node.servicesSummary?.apid || 'Unknown'}`"><i :class="serviceState('apid')" />apid</span>
      </div>
    </td>
    <td class="actions-cell">
      <div class="flex justify-end gap-1">
        <button class="row-action" :title="t('node_btn_services')" @click="emit('open-services', node)"><ListChecks class="h-3.5 w-3.5" /></button>
        <button class="row-action" :title="t('node_btn_logs')" @click="emit('open-logs', node)"><Terminal class="h-3.5 w-3.5" /></button>
        <button class="row-action danger" :title="t('node_btn_reboot')" @click="emit('open-reboot', node)"><RotateCw class="h-3.5 w-3.5" /></button>
      </div>
    </td>
  </tr>
</template>

<style scoped>
tr { border-bottom: 1px solid var(--border); transition: background-color 100ms ease; }
tr:last-child { border-bottom: 0; }
tr:hover { background: var(--surface-hover); }
tr.unavailable { background: color-mix(in srgb, var(--danger-muted) 45%, transparent); }
td { height: 64px; padding: 8px 12px; color: var(--text-muted); font-size: 11px; vertical-align: middle; }
.node-cell { min-width: 180px; color: var(--text); }
.ip-button { display: flex; align-items: center; gap: 5px; margin-top: 4px; color: var(--text-faint); font-size: 10px; }
.ip-button:hover { color: var(--accent); }
.role-label { color: var(--text-muted); font-size: 11px; }
.metric-cell { min-width: 115px; color: var(--text); line-height: 1.4; }
.secondary { margin-top: 2px; color: var(--text-faint); font-size: 10px; }
.service-list { display: flex; min-width: 160px; flex-wrap: wrap; gap: 5px 10px; font-size: 9px; }
.service-list span { display: inline-flex; align-items: center; gap: 4px; color: var(--text-muted); }
.service-list i { width: 5px; height: 5px; border-radius: 50%; background: var(--text-faint); }
.service-list i.ok { background: var(--success); }
.service-list i.danger { background: var(--danger); }
.actions-cell { min-width: 124px; }
.row-action { display: flex; width: 30px; height: 30px; align-items: center; justify-content: center; border: 1px solid var(--border); border-radius: 5px; color: var(--text-muted); background: var(--surface-raised); }
.row-action:hover { color: var(--text); border-color: var(--border-strong); background: var(--surface-hover); }
.row-action.danger:hover { color: var(--danger); border-color: #673139; background: var(--danger-muted); }
</style>

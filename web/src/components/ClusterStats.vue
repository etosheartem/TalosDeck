<script setup lang="ts">
import { computed } from 'vue'
import { t } from '../i18n'
import type { ClusterInfo } from '../types'

const props = defineProps<{ cluster: ClusterInfo }>()

const unavailableNodes = computed(() => Math.max(0, props.cluster.totalNodes - props.cluster.readyNodes))
</script>

<template>
  <section class="panel overflow-hidden" aria-label="Cluster summary">
    <div class="grid grid-cols-2 divide-x divide-y lg:grid-cols-5 lg:divide-y-0" style="border-color: var(--border)">
      <div class="stat-cell col-span-2 lg:col-span-1">
        <span :class="['status-dot', cluster.healthy ? 'is-ok' : 'is-danger']" />
        <div>
          <div class="stat-label">{{ t('cluster_status') }}</div>
          <div :class="['stat-value', cluster.healthy ? 'healthy' : 'danger']">
            {{ cluster.healthy ? t('cluster_healthy') : t('cluster_degraded') }}
          </div>
        </div>
      </div>
      <div class="stat-cell">
        <div>
          <div class="stat-label">{{ t('stat_total_nodes') }}</div>
          <div class="stat-value mono">{{ cluster.totalNodes }}</div>
        </div>
      </div>
      <div class="stat-cell">
        <div>
          <div class="stat-label">{{ t('stat_healthy') }}</div>
          <div class="stat-value mono"><span class="healthy">{{ cluster.readyNodes }}</span><span class="muted"> / {{ cluster.totalNodes }}</span></div>
        </div>
      </div>
      <div class="stat-cell">
        <div>
          <div class="stat-label">{{ t('stat_control_plane') }}</div>
          <div class="stat-value mono">{{ cluster.controlPlaneCount }}</div>
        </div>
      </div>
      <div class="stat-cell">
        <div>
          <div class="stat-label">{{ t('stat_workers') }}</div>
          <div class="stat-value mono">{{ cluster.workerCount }}</div>
        </div>
        <span v-if="unavailableNodes" class="status-chip is-danger">{{ unavailableNodes }} unavailable</span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.stat-cell {
  display: flex;
  min-height: 70px;
  align-items: center;
  gap: 10px;
  padding: 12px 16px;
  border-color: var(--border);
}
.stat-label { color: var(--text-muted); font-size: 11px; }
.stat-value { margin-top: 3px; color: var(--text); font-size: 15px; font-weight: 650; }
.healthy { color: var(--success); }
.danger { color: var(--danger); }
.muted { color: var(--text-faint); }
</style>

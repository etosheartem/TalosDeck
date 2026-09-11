<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Search, Plus, TriangleAlert, Server } from 'lucide-vue-next'
import { t, currentLocale } from '../../i18n'
import { fetchProxmoxStatus } from '../../api'
import type { NodeOverview, ProxmoxStatusResponse, CreateWorkerResult } from '../../types'
import NodeCard from '../NodeCard.vue'
import AddWorkerModal from '../AddWorkerModal.vue'

const props = defineProps<{ nodes: NodeOverview[] }>()
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
const proxmox = ref<ProxmoxStatusResponse | null>(null)
const loadingProxmox = ref(false)

const loadProxmox = async () => {
  loadingProxmox.value = true
  try {
    proxmox.value = await fetchProxmoxStatus()
  } catch (error) {
    console.warn('Failed to load Proxmox status in NodesView:', error)
  } finally {
    loadingProxmox.value = false
  }
}

onMounted(loadProxmox)

const filteredNodes = computed(() => props.nodes.filter((node) => {
  const query = searchQuery.value.trim().toLowerCase()
  const matchesQuery = !query || node.hostname.toLowerCase().includes(query) || node.ip.includes(query)
  const matchesRole = roleFilter.value === 'all' || node.role === roleFilter.value
  return matchesQuery && matchesRole
}))

const formatBytes = (bytes?: number) => bytes == null ? '—' : `${(bytes / 1024 ** 3).toFixed(1)} GB`
const formatPercent = (value?: number) => value == null ? '—' : `${Math.round(value)}%`
const pve = computed(() => proxmox.value?.status)

const handleWorkerCreated = (result: CreateWorkerResult) => {
  isAddModalOpen.value = false
  emit('show-toast', { message: `${t('add_worker_success')}: ${result.name} (VMID ${result.vmid})`, type: 'success' })
  emit('refresh')
  loadProxmox()
}
</script>

<template>
  <div class="space-y-4">
    <section v-if="proxmox?.configured" class="panel host-strip">
      <div class="host-identity">
        <span class="status-dot is-ok" />
        <div>
          <div class="flex items-center gap-2 text-xs font-semibold">
            <span>{{ t('proxmox_title') }}</span>
            <span class="mono" style="color: var(--accent)">{{ proxmox.node || 'pve' }}</span>
          </div>
          <p class="mt-1 text-[10px]" style="color: var(--text-faint)">{{ t('proxmox_scale_desc') }}</p>
        </div>
      </div>
      <dl class="host-metrics mono">
        <div><dt>{{ t('proxmox_cpu_load') }}</dt><dd>{{ loadingProxmox ? '…' : formatPercent(pve?.cpuUsagePercent) }}</dd></div>
        <div><dt>{{ t('proxmox_free_ram') }}</dt><dd>{{ loadingProxmox ? '…' : formatBytes(pve?.memory?.available ?? pve?.memory?.free) }}</dd></div>
        <div><dt>{{ t('proxmox_free_disk') }}</dt><dd>{{ loadingProxmox ? '…' : formatBytes(pve?.storage?.free) }}</dd></div>
      </dl>
      <button class="primary-button" @click="isAddModalOpen = true"><Plus class="h-3.5 w-3.5" />{{ t('proxmox_add_worker_btn') }}</button>
    </section>

    <section>
      <div class="mb-3 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div class="flex items-baseline gap-2">
            <h2 class="text-sm font-semibold">{{ t('tab_nodes') }}</h2>
            <span class="mono text-[10px]" style="color: var(--text-faint)">{{ filteredNodes.length }} / {{ nodes.length }}</span>
          </div>
          <p class="mt-1 text-[11px]" style="color: var(--text-muted)">
            {{ currentLocale === 'ru' ? 'Состояние машин и системных сервисов' : 'Machine and system service state' }}
          </p>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <label class="control relative flex items-center">
            <Search class="pointer-events-none absolute left-2.5 h-3.5 w-3.5" style="color: var(--text-faint)" />
            <input v-model="searchQuery" class="h-8 w-52 bg-transparent pl-8 pr-2 text-xs outline-none" :placeholder="currentLocale === 'ru' ? 'Имя или IP' : 'Hostname or IP'" />
          </label>
          <div class="filter-group">
            <button :class="{ active: roleFilter === 'all' }" @click="roleFilter = 'all'">{{ t('services_filter_all') }}</button>
            <button :class="{ active: roleFilter === 'controlplane' }" @click="roleFilter = 'controlplane'">CP</button>
            <button :class="{ active: roleFilter === 'worker' }" @click="roleFilter = 'worker'">{{ t('stat_workers') }}</button>
          </div>
          <button v-if="proxmox?.configured" class="control flex h-8 items-center gap-1.5 px-2.5 text-xs" @click="isAddModalOpen = true">
            <Plus class="h-3.5 w-3.5" />{{ t('proxmox_add_worker_btn') }}
          </button>
        </div>
      </div>

      <div v-if="filteredNodes.length" class="panel overflow-x-auto">
        <table class="nodes-table">
          <thead>
            <tr>
              <th>{{ currentLocale === 'ru' ? 'Узел' : 'Node' }}</th>
              <th>{{ currentLocale === 'ru' ? 'Роль' : 'Role' }}</th>
              <th>{{ currentLocale === 'ru' ? 'Состояние' : 'State' }}</th>
              <th>{{ currentLocale === 'ru' ? 'Версии' : 'Versions' }}</th>
              <th>{{ currentLocale === 'ru' ? 'Нагрузка' : 'Usage' }}</th>
              <th>{{ t('node_uptime') }}</th>
              <th>{{ currentLocale === 'ru' ? 'Сервисы' : 'Services' }}</th>
              <th class="text-right">{{ currentLocale === 'ru' ? 'Действия' : 'Actions' }}</th>
            </tr>
          </thead>
          <tbody>
            <NodeCard
              v-for="node in filteredNodes"
              :key="node.ip"
              :node="node"
              @open-services="emit('open-services', $event)"
              @open-logs="emit('open-logs', $event)"
              @open-reboot="emit('open-reboot', $event)"
            />
          </tbody>
        </table>
      </div>

      <div v-else-if="nodes.length === 0" class="panel empty-state danger-state">
        <TriangleAlert class="h-5 w-5" />
        <div>
          <p class="text-xs font-semibold">{{ currentLocale === 'ru' ? 'Ноды недоступны' : 'Nodes unavailable' }}</p>
          <p>{{ currentLocale === 'ru' ? 'Проверьте Talos API и состояние виртуальных машин.' : 'Check the Talos API and virtual machine state.' }}</p>
        </div>
      </div>
      <div v-else class="panel empty-state">
        <Server class="h-5 w-5" />
        <div><p class="text-xs font-semibold">{{ t('nodes_empty_title') }}</p><p>{{ t('nodes_empty_hint') }}</p></div>
      </div>
    </section>

    <AddWorkerModal
      :open="isAddModalOpen"
      @close="isAddModalOpen = false"
      @success="handleWorkerCreated"
      @error="emit('show-toast', { message: $event || t('add_worker_error'), type: 'error' })"
    />
  </div>
</template>

<style scoped>
.host-strip { display: flex; min-height: 64px; align-items: center; gap: 20px; padding: 10px 12px; }
.host-identity { display: flex; min-width: 230px; flex: 1; align-items: center; gap: 10px; }
.host-metrics { display: flex; align-items: center; gap: 24px; }
.host-metrics div { min-width: 76px; }
.host-metrics dt { color: var(--text-faint); font-size: 9px; text-transform: uppercase; letter-spacing: .06em; }
.host-metrics dd { margin-top: 3px; color: var(--text); font-size: 11px; }
.primary-button { display: flex; min-height: 32px; align-items: center; gap: 6px; border-radius: 6px; padding: 0 12px; background: var(--accent); color: #08111f; font-size: 11px; font-weight: 700; }
.primary-button:hover { background: #91baff; }
.filter-group { display: flex; height: 32px; align-items: center; gap: 2px; border: 1px solid var(--border); border-radius: 6px; padding: 2px; background: var(--surface-raised); }
.filter-group button { height: 26px; border-radius: 4px; padding: 0 9px; color: var(--text-muted); font-size: 10px; }
.filter-group button:hover { color: var(--text); }
.filter-group button.active { background: var(--border); color: var(--text); }
.nodes-table { width: 100%; min-width: 1040px; border-collapse: collapse; table-layout: auto; }
th { height: 34px; padding: 0 12px; border-bottom: 1px solid var(--border); color: var(--text-faint); font-size: 9px; font-weight: 650; letter-spacing: .06em; text-align: left; text-transform: uppercase; }
.empty-state { display: flex; min-height: 120px; align-items: center; justify-content: center; gap: 10px; padding: 24px; color: var(--text-muted); font-size: 11px; }
.empty-state p + p { margin-top: 3px; color: var(--text-faint); }
.danger-state { color: var(--danger); border-color: #553038; background: color-mix(in srgb, var(--danger-muted) 40%, var(--surface)); }
@media (max-width: 900px) {
  .host-strip { align-items: flex-start; flex-wrap: wrap; }
  .host-metrics { order: 3; width: 100%; justify-content: space-between; border-top: 1px solid var(--border); padding-top: 10px; }
}
</style>

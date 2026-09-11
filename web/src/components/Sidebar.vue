<script setup lang="ts">
import { Server, HardDrive, FileCode2, Boxes, Wrench, Layers3, X } from 'lucide-vue-next'
import { t, currentLocale, setLocale } from '../i18n'
import type { TabKey, ClusterInfo } from '../types'

defineProps<{
  activeTab: TabKey
  nodeCount: number
  podCount: number
  etcdHealthy: boolean
  cluster: ClusterInfo
  mobileOpen: boolean
}>()

const emit = defineEmits<{
  (e: 'update:activeTab', tab: TabKey): void
  (e: 'closeMobile'): void
}>()

const navItems: { key: TabKey; labelKey: string; icon: any; badge?: 'nodes' | 'pods' | 'etcd' }[] = [
  { key: 'nodes', labelKey: 'tab_nodes', icon: Server, badge: 'nodes' },
  { key: 'workloads', labelKey: 'tab_workloads', icon: Boxes, badge: 'pods' },
  { key: 'storage', labelKey: 'tab_storage', icon: HardDrive },
  { key: 'config', labelKey: 'tab_config', icon: FileCode2 },
  { key: 'operations', labelKey: 'tab_operations', icon: Wrench, badge: 'etcd' },
]

const selectTab = (key: TabKey) => {
  emit('update:activeTab', key)
  emit('closeMobile')
}
</script>

<template>
  <div>
    <button
      v-if="mobileOpen"
      class="fixed inset-0 z-40 bg-black/70 lg:hidden"
      aria-label="Close navigation"
      @click="emit('closeMobile')"
    />

    <aside
      :class="[
        'fixed inset-y-0 left-0 z-50 flex w-56 flex-col border-r transition-transform duration-200 lg:translate-x-0',
        mobileOpen ? 'translate-x-0' : '-translate-x-full',
      ]"
      style="background: var(--sidebar); border-color: var(--border)"
    >
      <div class="flex h-14 items-center justify-between border-b px-4" style="border-color: var(--border)">
        <div class="flex items-center gap-2.5">
          <div class="flex h-7 w-7 items-center justify-center rounded-md border" style="border-color: var(--border-strong); background: var(--surface-raised)">
            <Layers3 class="h-4 w-4" style="color: var(--accent)" />
          </div>
          <div class="leading-none">
            <div class="text-sm font-semibold tracking-tight">TalosDeck</div>
            <div class="mt-1 text-[10px] uppercase tracking-[0.12em]" style="color: var(--text-faint)">Cluster console</div>
          </div>
        </div>
        <button class="control flex h-8 w-8 items-center justify-center lg:hidden" @click="emit('closeMobile')">
          <X class="h-4 w-4" />
        </button>
      </div>

      <div class="border-b px-4 py-3" style="border-color: var(--border)">
        <div class="flex items-center justify-between gap-3">
          <span class="mono truncate text-xs font-semibold">{{ cluster.name }}</span>
          <span :class="['status-chip', cluster.healthy ? 'is-ok' : 'is-danger']">
            <span :class="['status-dot', cluster.healthy ? 'is-ok' : 'is-danger']" />
            {{ cluster.healthy ? t('cluster_healthy') : t('cluster_degraded') }}
          </span>
        </div>
        <div class="mono mt-2 flex gap-3 text-[10px]" style="color: var(--text-muted)">
          <span>Talos {{ cluster.talosVersion }}</span>
          <span>K8s {{ cluster.kubernetesVersion }}</span>
        </div>
      </div>

      <nav class="flex-1 space-y-1 overflow-y-auto p-2" :aria-label="t('sidebar_nav')">
        <button
          v-for="item in navItems"
          :key="item.key"
          :class="['nav-item', { active: activeTab === item.key }]"
          @click="selectTab(item.key)"
        >
          <component :is="item.icon" class="h-4 w-4" />
          <span class="flex-1 text-left">{{ t(item.labelKey) }}</span>
          <span v-if="item.badge === 'nodes' && nodeCount" class="mono nav-count">{{ nodeCount }}</span>
          <span v-if="item.badge === 'pods' && podCount" class="mono nav-count">{{ podCount }}</span>
          <span v-if="item.badge === 'etcd'" :class="['status-dot', etcdHealthy ? 'is-ok' : 'is-warning']" />
        </button>
      </nav>

      <div class="border-t p-3" style="border-color: var(--border)">
        <div class="flex items-center justify-between text-[11px]" style="color: var(--text-muted)">
          <span>Language</span>
          <div class="flex rounded-md border p-0.5" style="border-color: var(--border); background: var(--canvas)">
            <button :class="['locale-button', { active: currentLocale === 'ru' }]" @click="setLocale('ru')">RU</button>
            <button :class="['locale-button', { active: currentLocale === 'en' }]" @click="setLocale('en')">EN</button>
          </div>
        </div>
      </div>
    </aside>
  </div>
</template>

<style scoped>
.nav-item {
  display: flex;
  width: 100%;
  min-height: 36px;
  align-items: center;
  gap: 10px;
  border: 1px solid transparent;
  border-radius: 6px;
  padding: 0 10px;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 550;
  transition: background-color 120ms ease, color 120ms ease, border-color 120ms ease;
}

.nav-item:hover { background: var(--surface); color: var(--text); }
.nav-item.active { background: var(--accent-muted); border-color: #28446f; color: #b9d3ff; }
.nav-count { color: var(--text-faint); font-size: 10px; }
.nav-item.active .nav-count { color: #8db6fa; }

.locale-button {
  min-width: 30px;
  border-radius: 4px;
  padding: 3px 6px;
  color: var(--text-faint);
  font-size: 10px;
  font-weight: 700;
}

.locale-button.active { background: var(--surface-raised); color: var(--text); }
</style>

<script setup lang="ts">
import {
  Menu,
  RefreshCw,
  Server,
  HardDrive,
  FileCode2,
  Boxes,
  Zap,
} from 'lucide-vue-next'
import { t } from '../i18n'
import type { TabKey, ClusterInfo } from '../types'

defineProps<{
  activeTab: TabKey
  cluster: ClusterInfo
  loading: boolean
  autoRefreshInterval: number
}>()

const emit = defineEmits<{
  (e: 'toggleMobile'): void
  (e: 'refresh'): void
  (e: 'update:autoRefreshInterval', val: number): void
}>()

const autoRefreshOptions = [
  { value: 0, labelKey: 'auto_refresh_off' },
  { value: 5000, labelKey: 'auto_refresh_5s' },
  { value: 10000, labelKey: 'auto_refresh_10s' },
  { value: 30000, labelKey: 'auto_refresh_30s' },
]

const tabTitles: Record<TabKey, { labelKey: string; icon: any }> = {
  nodes: { labelKey: 'tab_nodes', icon: Server },
  storage: { labelKey: 'tab_storage', icon: HardDrive },
  config: { labelKey: 'tab_config', icon: FileCode2 },
  workloads: { labelKey: 'tab_workloads', icon: Boxes },
  operations: { labelKey: 'tab_operations', icon: Zap },
}

const onAutoRefreshChange = (e: Event) => {
  const target = e.target as HTMLSelectElement
  emit('update:autoRefreshInterval', Number(target.value))
}
</script>

<template>
  <header class="border-b border-zinc-800/80 bg-zinc-950/80 backdrop-blur-md sticky top-0 z-20 px-4 sm:px-6 lg:px-8 py-3 flex items-center justify-between gap-4">
    <!-- Left: Mobile Toggle & Breadcrumbs -->
    <div class="flex items-center gap-3">
      <button
        @click="emit('toggleMobile')"
        class="lg:hidden p-1.5 rounded-lg text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900 border border-zinc-800"
      >
        <Menu class="w-5 h-5" />
      </button>

      <div class="flex items-center gap-2 text-sm font-semibold">
        <component
          :is="tabTitles[activeTab]?.icon"
          class="w-4 h-4 text-cyan-400"
        />
        <span class="text-zinc-100">{{ t(tabTitles[activeTab]?.labelKey || 'tab_nodes') }}</span>
        <span class="text-zinc-600 hidden sm:inline">•</span>
        <span class="text-xs font-mono text-zinc-500 font-normal hidden sm:inline">{{ cluster.name }} ({{ cluster.endpoint }})</span>
      </div>
    </div>

    <!-- Right: Controls -->
    <div class="flex items-center gap-2.5">
      <!-- Auto Refresh Select -->
      <div class="hidden sm:flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-zinc-900/90 border border-zinc-800 text-xs">
        <span class="text-zinc-400">{{ t('auto_refresh') }}:</span>
        <select
          :value="autoRefreshInterval"
          @change="onAutoRefreshChange"
          class="bg-transparent text-zinc-200 font-medium focus:outline-none cursor-pointer"
        >
          <option
            v-for="opt in autoRefreshOptions"
            :key="opt.value"
            :value="opt.value"
            class="bg-zinc-900 text-zinc-200"
          >
            {{ t(opt.labelKey) }}
          </option>
        </select>
      </div>

      <!-- Manual Refresh Button -->
      <button
        @click="emit('refresh')"
        :disabled="loading"
        :class="[
          'flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-semibold transition-all cursor-pointer border select-none',
          loading
            ? 'bg-zinc-900 text-zinc-500 border-zinc-800 cursor-not-allowed'
            : 'bg-cyan-950/60 text-cyan-300 border-cyan-800/80 hover:bg-cyan-900/60 hover:border-cyan-600 shadow-sm shadow-cyan-950/40',
        ]"
      >
        <RefreshCw :class="['w-3.5 h-3.5', loading ? 'animate-spin text-zinc-500' : 'text-cyan-400']" />
        <span class="hidden sm:inline">{{ t('refresh') }}</span>
      </button>
    </div>
  </header>
</template>

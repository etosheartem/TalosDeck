<script setup lang="ts">
import {
  Server,
  HardDrive,
  FileCode2,
  Boxes,
  Zap,
} from 'lucide-vue-next'
import { t } from '../i18n'
import type { TabKey } from '../types'

defineProps<{
  activeTab: TabKey
  nodeCount: number
  podCount: number
  etcdHealthy: boolean
}>()

const emit = defineEmits<{
  (e: 'update:activeTab', tab: TabKey): void
}>()

const tabs: { key: TabKey; labelKey: string; icon: any }[] = [
  { key: 'nodes', labelKey: 'tab_nodes', icon: Server },
  { key: 'storage', labelKey: 'tab_storage', icon: HardDrive },
  { key: 'config', labelKey: 'tab_config', icon: FileCode2 },
  { key: 'workloads', labelKey: 'tab_workloads', icon: Boxes },
  { key: 'operations', labelKey: 'tab_operations', icon: Zap },
]
</script>

<template>
  <nav class="border-b border-zinc-800/80 bg-zinc-950/90 backdrop-blur-md sticky top-[69px] z-20 px-4 lg:px-8">
    <div class="max-w-7xl mx-auto flex items-center gap-1 sm:gap-2 overflow-x-auto no-scrollbar py-2">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        @click="emit('update:activeTab', tab.key)"
        :class="[
          'flex items-center gap-2 px-3.5 py-2 rounded-xl text-xs sm:text-sm font-semibold transition-all duration-200 cursor-pointer whitespace-nowrap select-none group',
          activeTab === tab.key
            ? 'bg-zinc-900 text-cyan-300 border border-cyan-500/40 shadow-lg shadow-cyan-950/30'
            : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900/60 border border-transparent',
        ]"
      >
        <component
          :is="tab.icon"
          :class="[
            'w-4 h-4 transition-colors',
            activeTab === tab.key
              ? 'text-cyan-400'
              : 'text-zinc-500 group-hover:text-zinc-300',
          ]"
        />
        <span>{{ t(tab.labelKey) }}</span>

        <!-- Node count badge on nodes tab -->
        <span
          v-if="tab.key === 'nodes' && nodeCount > 0"
          :class="[
            'px-1.5 py-0.2 rounded-full text-[10px] font-mono font-bold transition-colors',
            activeTab === 'nodes'
              ? 'bg-cyan-950 text-cyan-300 border border-cyan-800/60'
              : 'bg-zinc-800 text-zinc-400 border border-zinc-700/60',
          ]"
        >
          {{ nodeCount }}
        </span>

        <!-- Pod count badge on workloads tab -->
        <span
          v-if="tab.key === 'workloads' && podCount > 0"
          :class="[
            'px-1.5 py-0.2 rounded-full text-[10px] font-mono font-bold transition-colors',
            activeTab === 'workloads'
              ? 'bg-cyan-950 text-cyan-300 border border-cyan-800/60'
              : 'bg-zinc-800 text-zinc-400 border border-zinc-700/60',
          ]"
        >
          {{ podCount }}
        </span>

        <!-- etcd status indicator on operations tab -->
        <span
          v-if="tab.key === 'operations'"
          class="relative flex h-2 w-2 ml-0.5"
        >
          <span
            v-if="etcdHealthy"
            class="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"
          ></span>
          <span
            :class="[
              'relative inline-flex rounded-full h-2 w-2',
              etcdHealthy ? 'bg-emerald-400' : 'bg-amber-400',
            ]"
          ></span>
        </span>
      </button>
    </div>
  </nav>
</template>

<style scoped>
.no-scrollbar::-webkit-scrollbar {
  display: none;
}
.no-scrollbar {
  -ms-overflow-style: none;
  scrollbar-width: none;
}
</style>

<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  Server,
  Search,
  Shield,
  Cpu,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type { NodeOverview } from '../../types'
import NodeCard from '../NodeCard.vue'

const props = defineProps<{
  nodes: NodeOverview[]
}>()

const emit = defineEmits<{
  (e: 'open-services', node: NodeOverview): void
  (e: 'open-logs', node: NodeOverview): void
  (e: 'open-reboot', node: NodeOverview): void
}>()

const searchQuery = ref('')
const roleFilter = ref<'all' | 'controlplane' | 'worker'>('all')

const filteredNodes = computed(() => {
  return props.nodes.filter((node) => {
    const q = searchQuery.value.trim().toLowerCase()
    const matchesQuery = !q || node.hostname.toLowerCase().includes(q) || node.ip.includes(q)
    if (!matchesQuery) return false

    if (roleFilter.value !== 'all' && node.role !== roleFilter.value) {
      return false
    }

    return true
  })
})
</script>

<template>
  <div class="space-y-4">
    <!-- Controls / Filter Bar -->
    <div class="flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3 pt-1">
      <!-- Section Heading -->
      <div class="flex items-center gap-2.5">
        <h2 class="text-lg font-bold text-zinc-100 tracking-tight">
          {{ t('tab_nodes') }}
        </h2>
        <span class="px-2 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-800 text-zinc-300 border border-zinc-700/60">
          {{ filteredNodes.length }}
        </span>
      </div>

      <!-- Filters: Search + Role Pills -->
      <div class="flex flex-wrap items-center gap-2.5">
        <!-- Search -->
        <div class="relative min-w-[200px] flex-1 sm:flex-none">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-zinc-500" />
          <input
            v-model="searchQuery"
            type="text"
            placeholder="Filter by hostname or IP..."
            class="w-full sm:w-56 pl-8 pr-3 py-1.5 rounded-lg bg-zinc-900 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-cyan-500/70"
          />
        </div>

        <!-- Role tabs -->
        <div class="flex items-center rounded-lg bg-zinc-900 border border-zinc-800 p-0.5 text-xs font-medium">
          <button
            @click="roleFilter = 'all'"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer',
              roleFilter === 'all'
                ? 'bg-zinc-800 text-white shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            {{ t('services_filter_all') }}
          </button>
          <button
            @click="roleFilter = 'controlplane'"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer flex items-center gap-1',
              roleFilter === 'controlplane'
                ? 'bg-zinc-800 text-violet-300 shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            <Shield class="w-3 h-3" />
            <span>CP</span>
          </button>
          <button
            @click="roleFilter = 'worker'"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer flex items-center gap-1',
              roleFilter === 'worker'
                ? 'bg-zinc-800 text-sky-300 shadow-sm'
                : 'text-zinc-400 hover:text-zinc-200',
            ]"
          >
            <Cpu class="w-3 h-3" />
            <span>Workers</span>
          </button>
        </div>
      </div>
    </div>

    <!-- Node Cards Grid -->
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4 lg:gap-5">
      <NodeCard
        v-for="node in filteredNodes"
        :key="node.ip"
        :node="node"
        @open-services="emit('open-services', $event)"
        @open-logs="emit('open-logs', $event)"
        @open-reboot="emit('open-reboot', $event)"
      />
    </div>

    <!-- Empty state when search matches nothing -->
    <div
      v-if="filteredNodes.length === 0"
      class="py-16 text-center rounded-2xl bg-zinc-900/40 border border-zinc-800/60"
    >
      <Server class="w-10 h-10 mx-auto text-zinc-600 mb-3" />
      <p class="text-sm font-semibold text-zinc-300">No nodes found matching criteria</p>
      <p class="text-xs text-zinc-500 mt-1">Try clearing filters or search queries</p>
    </div>
  </div>
</template>

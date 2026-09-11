<script setup lang="ts">
import { Server, Shield, Cpu, CheckCircle2 } from 'lucide-vue-next'
import { t } from '../i18n'
import type { ClusterInfo } from '../types'

defineProps<{
  cluster: ClusterInfo
}>()
</script>

<template>
  <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 md:gap-4">
    <!-- Total Nodes -->
    <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
      <div class="p-2.5 rounded-lg bg-zinc-800 text-cyan-400 border border-zinc-700/50">
        <Server class="w-5 h-5" />
      </div>
      <div>
        <p class="text-xs font-medium text-zinc-400">{{ t('stat_total_nodes') }}</p>
        <div class="flex items-baseline gap-1.5 mt-0.5">
          <span class="text-2xl font-bold tracking-tight text-zinc-100 font-mono">{{ cluster.totalNodes }}</span>
          <span class="text-[11px] text-zinc-500 font-normal">nodes</span>
        </div>
      </div>
    </div>

    <!-- Control Plane -->
    <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
      <div class="p-2.5 rounded-lg bg-violet-950/60 text-violet-400 border border-violet-800/50">
        <Shield class="w-5 h-5" />
      </div>
      <div>
        <p class="text-xs font-medium text-zinc-400">{{ t('stat_control_plane') }}</p>
        <div class="flex items-baseline gap-1.5 mt-0.5">
          <span class="text-2xl font-bold tracking-tight text-violet-300 font-mono">{{ cluster.controlPlaneCount }}</span>
          <span class="text-[11px] text-zinc-500 font-normal">masters</span>
        </div>
      </div>
    </div>

    <!-- Workers -->
    <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
      <div class="p-2.5 rounded-lg bg-sky-950/60 text-sky-400 border border-sky-800/50">
        <Cpu class="w-5 h-5" />
      </div>
      <div>
        <p class="text-xs font-medium text-zinc-400">{{ t('stat_workers') }}</p>
        <div class="flex items-baseline gap-1.5 mt-0.5">
          <span class="text-2xl font-bold tracking-tight text-sky-300 font-mono">{{ cluster.workerCount }}</span>
          <span class="text-[11px] text-zinc-500 font-normal">workers</span>
        </div>
      </div>
    </div>

    <!-- Healthy Status -->
    <div class="bg-zinc-900/70 backdrop-blur border border-zinc-800/80 rounded-xl p-4 flex items-center gap-3.5 shadow-sm hover:border-zinc-700/80 transition-all">
      <div class="p-2.5 rounded-lg bg-emerald-950/60 text-emerald-400 border border-emerald-800/50">
        <CheckCircle2 class="w-5 h-5" />
      </div>
      <div>
        <p class="text-xs font-medium text-zinc-400">{{ t('stat_healthy') }}</p>
        <div class="flex items-baseline gap-1.5 mt-0.5">
          <span class="text-2xl font-bold tracking-tight text-emerald-400 font-mono">{{ cluster.readyNodes }}/{{ cluster.totalNodes }}</span>
          <span class="text-[11px] text-emerald-500/80 font-medium">Ready</span>
        </div>
      </div>
    </div>
  </div>
</template>

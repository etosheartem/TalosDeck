<script setup lang="ts">
import {
  ShieldCheck,
  AlertTriangle,
  RefreshCw,
  Layers,
  Activity,
} from 'lucide-vue-next'
import { t, currentLocale, setLocale } from '../i18n'
import type { ClusterInfo } from '../types'

defineProps<{
  cluster: ClusterInfo
  loading: boolean
  autoRefreshInterval: number
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
  (e: 'update:autoRefreshInterval', value: number): void
}>()

const autoRefreshOptions = [
  { value: 0, labelKey: 'auto_refresh_off' },
  { value: 5000, labelKey: 'auto_refresh_5s' },
  { value: 10000, labelKey: 'auto_refresh_10s' },
  { value: 30000, labelKey: 'auto_refresh_30s' },
]

const onAutoRefreshChange = (e: Event) => {
  const target = e.target as HTMLSelectElement
  emit('update:autoRefreshInterval', Number(target.value))
}
</script>

<template>
  <header class="border-b border-zinc-800 bg-zinc-950/80 backdrop-blur-md sticky top-0 z-30 px-4 lg:px-8 py-3.5">
    <div class="max-w-7xl mx-auto flex flex-col md:flex-row md:items-center md:justify-between gap-4">
      <!-- Left: Brand & Cluster Health -->
      <div class="flex items-center gap-4 flex-wrap">
        <div class="flex items-center gap-3">
          <div class="h-10 w-10 rounded-xl bg-gradient-to-tr from-cyan-600 via-teal-500 to-emerald-400 p-[1.5px] shadow-lg shadow-cyan-500/20">
            <div class="h-full w-full bg-zinc-950 rounded-[10px] flex items-center justify-center">
              <Layers class="w-5 h-5 text-cyan-400" />
            </div>
          </div>
          <div>
            <div class="flex items-center gap-2">
              <span class="text-xl font-bold tracking-tight bg-gradient-to-r from-zinc-100 via-zinc-200 to-zinc-400 bg-clip-text text-transparent">
                {{ t('app_title') }}
              </span>
              <span class="text-[10px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-cyan-950/80 text-cyan-400 border border-cyan-800/60">
                MVP
              </span>
            </div>
            <p class="text-xs text-zinc-400 hidden sm:block">
              {{ t('app_subtitle') }}
            </p>
          </div>
        </div>

        <!-- Cluster Status Pill -->
        <div class="h-6 w-[1px] bg-zinc-800 hidden md:block"></div>

        <div class="flex items-center gap-2">
          <!-- Cluster name badge -->
          <div class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-zinc-900 border border-zinc-800 text-xs font-medium">
            <span class="text-zinc-400">{{ t('cluster_status') }}:</span>
            <span class="text-zinc-100 font-semibold tracking-wide">{{ cluster.name }}</span>
            <span
              :class="[
                'inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-semibold tracking-wide border',
                cluster.healthy
                  ? 'bg-emerald-950/60 text-emerald-400 border-emerald-800/60 shadow-[0_0_12px_rgba(16,185,129,0.25)]'
                  : 'bg-amber-950/60 text-amber-400 border-amber-800/60',
              ]"
            >
              <span
                :class="[
                  'w-1.5 h-1.5 rounded-full',
                  cluster.healthy ? 'bg-emerald-400 animate-pulse' : 'bg-amber-400',
                ]"
              ></span>
              <ShieldCheck v-if="cluster.healthy" class="w-3 h-3" />
              <AlertTriangle v-else class="w-3 h-3" />
              {{ cluster.healthy ? t('cluster_healthy') : t('cluster_degraded') }}
            </span>
          </div>

          <!-- Version Badges -->
          <div class="hidden lg:flex items-center gap-2 text-xs">
            <div class="px-2.5 py-1 rounded-lg bg-zinc-900/90 border border-zinc-800 text-zinc-300 flex items-center gap-1.5">
              <span class="text-zinc-500">{{ t('talos_version') }}:</span>
              <span class="font-mono text-cyan-400 font-medium">{{ cluster.talosVersion }}</span>
            </div>
            <div class="px-2.5 py-1 rounded-lg bg-zinc-900/90 border border-zinc-800 text-zinc-300 flex items-center gap-1.5">
              <span class="text-zinc-500">{{ t('k8s_version') }}:</span>
              <span class="font-mono text-emerald-400 font-medium">{{ cluster.kubernetesVersion }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Right: Controls (Auto refresh, manual refresh, Language switch) -->
      <div class="flex items-center gap-2.5 self-end md:self-center">
        <!-- Auto Refresh Select -->
        <div class="flex items-center gap-1.5 bg-zinc-900 border border-zinc-800 rounded-lg px-2 py-1 text-xs text-zinc-300">
          <Activity class="w-3.5 h-3.5 text-zinc-400" />
          <span class="text-zinc-500 hidden sm:inline">{{ t('auto_refresh') }}:</span>
          <select
            :value="autoRefreshInterval"
            @change="onAutoRefreshChange"
            class="bg-transparent text-zinc-200 text-xs font-medium focus:outline-none cursor-pointer pr-1"
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
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-medium text-zinc-200 transition-all active:scale-95 disabled:opacity-50 cursor-pointer shadow-sm"
          :title="t('refresh')"
        >
          <RefreshCw :class="['w-3.5 h-3.5 text-cyan-400', loading ? 'animate-spin' : '']" />
          <span class="hidden sm:inline">{{ loading ? t('refreshing') : t('refresh') }}</span>
        </button>

        <!-- Language Switcher RU / EN -->
        <div class="flex items-center rounded-lg bg-zinc-900 border border-zinc-800 p-0.5 text-xs font-medium">
          <button
            @click="setLocale('ru')"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer font-semibold',
              currentLocale === 'ru'
                ? 'bg-gradient-to-r from-cyan-600 to-teal-600 text-white shadow-md shadow-cyan-900/40'
                : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/50',
            ]"
          >
            RU
          </button>
          <button
            @click="setLocale('en')"
            :class="[
              'px-2.5 py-1 rounded-md transition-all cursor-pointer font-semibold',
              currentLocale === 'en'
                ? 'bg-gradient-to-r from-cyan-600 to-teal-600 text-white shadow-md shadow-cyan-900/40'
                : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/50',
            ]"
          >
            EN
          </button>
        </div>
      </div>
    </div>
  </header>
</template>

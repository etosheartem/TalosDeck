<script setup lang="ts">
import {
  Server,
  HardDrive,
  FileCode2,
  Boxes,
  Zap,
  Layers,
  ChevronRight,
  X,
} from 'lucide-vue-next'
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

const navItems: { key: TabKey; labelKey: string; icon: any; badgeType?: 'nodes' | 'pods' | 'etcd' }[] = [
  { key: 'nodes', labelKey: 'tab_nodes', icon: Server, badgeType: 'nodes' },
  { key: 'storage', labelKey: 'tab_storage', icon: HardDrive },
  { key: 'config', labelKey: 'tab_config', icon: FileCode2 },
  { key: 'workloads', labelKey: 'tab_workloads', icon: Boxes, badgeType: 'pods' },
  { key: 'operations', labelKey: 'tab_operations', icon: Zap, badgeType: 'etcd' },
]

const selectTab = (key: TabKey) => {
  emit('update:activeTab', key)
  emit('closeMobile')
}
</script>

<template>
  <div>
    <!-- Mobile Backdrop -->
    <div
      v-if="mobileOpen"
      @click="emit('closeMobile')"
      class="fixed inset-0 bg-black/70 backdrop-blur-sm z-40 lg:hidden transition-opacity"
    ></div>

    <!-- Sidebar Container -->
    <aside
      :class="[
        'fixed top-0 bottom-0 left-0 w-64 bg-zinc-950 border-r border-zinc-800/80 flex flex-col z-50 transition-transform duration-300 ease-in-out lg:translate-x-0',
        mobileOpen ? 'translate-x-0 shadow-2xl' : '-translate-x-full',
      ]"
    >
      <!-- Brand / Header -->
      <div class="p-5 border-b border-zinc-800/80 flex items-center justify-between">
        <div class="flex items-center gap-3">
          <div class="h-9 w-9 rounded-xl bg-gradient-to-tr from-cyan-600 via-teal-500 to-emerald-400 p-[1.5px] shadow-lg shadow-cyan-500/20">
            <div class="h-full w-full bg-zinc-950 rounded-[10px] flex items-center justify-center">
              <Layers class="w-4 h-4 text-cyan-400" />
            </div>
          </div>
          <div>
            <div class="flex items-center gap-1.5">
              <span class="text-base font-bold tracking-tight bg-gradient-to-r from-zinc-100 via-zinc-200 to-zinc-400 bg-clip-text text-transparent">
                TalosDeck
              </span>
              <span class="text-[9px] font-mono uppercase tracking-wider px-1 py-0.2 rounded bg-cyan-950/80 text-cyan-400 border border-cyan-800/60 font-semibold">
                v0.2
              </span>
            </div>
            <p class="text-[11px] text-zinc-400 font-medium">
              Control Plane
            </p>
          </div>
        </div>

        <!-- Mobile close button -->
        <button
          @click="emit('closeMobile')"
          class="lg:hidden p-1.5 rounded-lg text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900 cursor-pointer"
          :title="t('close')"
          :aria-label="t('close')"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Cluster Quick Status Widget -->
      <div class="p-3 mx-3 mt-3 rounded-xl bg-zinc-900/80 border border-zinc-800/80">
        <div class="flex items-center justify-between text-xs mb-1.5">
          <span class="text-zinc-400 font-medium">{{ t('cluster_status') }}</span>
          <span
            :class="[
              'inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-semibold tracking-wide border',
              cluster.healthy
                ? 'bg-emerald-950/60 text-emerald-400 border-emerald-800/60'
                : 'bg-amber-950/60 text-amber-400 border-amber-800/60',
            ]"
          >
            <span
              :class="[
                'w-1.5 h-1.5 rounded-full',
                cluster.healthy ? 'bg-emerald-400 animate-pulse' : 'bg-amber-400',
              ]"
            ></span>
            {{ cluster.healthy ? t('cluster_healthy') : t('cluster_degraded') }}
          </span>
        </div>
        <div class="text-xs font-mono font-semibold text-zinc-200 truncate">
          {{ cluster.name }}
        </div>
        <div class="flex items-center gap-2 mt-2 pt-2 border-t border-zinc-800/60 text-[10px] font-mono text-zinc-400">
          <div>Talos <span class="text-cyan-400">{{ cluster.talosVersion }}</span></div>
          <span>•</span>
          <div>K8s <span class="text-emerald-400">{{ cluster.kubernetesVersion }}</span></div>
        </div>
      </div>

      <!-- Navigation Links -->
      <div class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
        <div class="px-3 py-1 text-[10px] font-semibold tracking-wider text-zinc-500 uppercase">
          {{ t('sidebar_nav') }}
        </div>

        <button
          v-for="item in navItems"
          :key="item.key"
          @click="selectTab(item.key)"
          :class="[
            'w-full flex items-center justify-between px-3 py-2.5 rounded-xl text-xs font-semibold transition-all duration-150 cursor-pointer group text-left',
            activeTab === item.key
              ? 'bg-gradient-to-r from-cyan-950/70 to-zinc-900 text-cyan-300 border border-cyan-500/40 shadow-md shadow-cyan-950/20'
              : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900/60 border border-transparent',
          ]"
        >
          <div class="flex items-center gap-2.5">
            <component
              :is="item.icon"
              :class="[
                'w-4 h-4 transition-colors',
                activeTab === item.key ? 'text-cyan-400' : 'text-zinc-500 group-hover:text-zinc-300',
              ]"
            />
            <span>{{ t(item.labelKey) }}</span>
          </div>

          <!-- Badges -->
          <div class="flex items-center gap-1.5">
            <span
              v-if="item.badgeType === 'nodes' && nodeCount > 0"
              :class="[
                'px-1.5 py-0.2 rounded-full text-[10px] font-mono font-bold',
                activeTab === 'nodes'
                  ? 'bg-cyan-900/80 text-cyan-200 border border-cyan-700/60'
                  : 'bg-zinc-800 text-zinc-400 border border-zinc-700/60',
              ]"
            >
              {{ nodeCount }}
            </span>

            <span
              v-if="item.badgeType === 'pods' && podCount > 0"
              :class="[
                'px-1.5 py-0.2 rounded-full text-[10px] font-mono font-bold',
                activeTab === 'workloads'
                  ? 'bg-cyan-900/80 text-cyan-200 border border-cyan-700/60'
                  : 'bg-zinc-800 text-zinc-400 border border-zinc-700/60',
              ]"
            >
              {{ podCount }}
            </span>

            <span
              v-if="item.badgeType === 'etcd'"
              class="relative flex h-2 w-2"
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

            <ChevronRight
              v-if="activeTab === item.key"
              class="w-3.5 h-3.5 text-cyan-400"
            />
          </div>
        </button>
      </div>

      <!-- Bottom: Language Switcher & GitHub Link -->
      <div class="p-3 border-t border-zinc-800/80 space-y-2 bg-zinc-950/60">
        <!-- Language Switcher -->
        <div class="flex items-center justify-between px-2 py-1.5 rounded-lg bg-zinc-900/80 border border-zinc-800/80">
          <span class="text-[11px] text-zinc-400 font-medium">Язык / Language</span>
          <div class="flex items-center p-0.5 rounded-md bg-zinc-950 border border-zinc-800 text-xs font-semibold">
            <button
              @click="setLocale('ru')"
              :class="[
                'px-2 py-0.5 rounded text-[10px] font-medium transition-colors cursor-pointer',
                currentLocale === 'ru'
                  ? 'bg-cyan-500 text-zinc-950 font-bold shadow-sm'
                  : 'text-zinc-400 hover:text-zinc-200',
              ]"
            >
              RU
            </button>
            <button
              @click="setLocale('en')"
              :class="[
                'px-2 py-0.5 rounded text-[10px] font-medium transition-colors cursor-pointer',
                currentLocale === 'en'
                  ? 'bg-cyan-500 text-zinc-950 font-bold shadow-sm'
                  : 'text-zinc-400 hover:text-zinc-200',
              ]"
            >
              EN
            </button>
          </div>
        </div>
      </div>
    </aside>
  </div>
</template>

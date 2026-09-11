<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import {
  X,
  Search,
  RefreshCw,
  Server,
  CheckCircle2,
  AlertCircle,
} from 'lucide-vue-next'
import { t } from '../i18n'
import { fetchNodeServices } from '../api'
import type { NodeOverview, TalosService } from '../types'

const props = defineProps<{
  node: NodeOverview | null
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const services = ref<TalosService[]>([])
const loading = ref(false)
const searchQuery = ref('')
const filterState = ref<'all' | 'running' | 'issues'>('all')
let requestGeneration = 0

const loadServices = async () => {
  if (!props.node) return
  const nodeIP = props.node.ip
  const isCP = props.node.role === 'controlplane'
  const generation = ++requestGeneration
  loading.value = true
  try {
    const result = await fetchNodeServices(nodeIP, isCP)
    if (generation === requestGeneration && props.open && props.node?.ip === nodeIP) {
      services.value = result
    }
  } catch (err) {
    console.error('Failed to load services', err)
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

watch(
  () => [props.open, props.node?.ip] as const,
  ([isOpen]) => {
    if (isOpen) {
      searchQuery.value = ''
      filterState.value = 'all'
      loadServices()
    } else {
      requestGeneration++
      loading.value = false
    }
  },
  { immediate: true }
)

const filteredServices = computed(() => {
  return services.value.filter((svc) => {
    // Search query filter
    const matchesSearch =
      searchQuery.value.trim() === '' ||
      svc.name.toLowerCase().includes(searchQuery.value.toLowerCase()) ||
      svc.description.toLowerCase().includes(searchQuery.value.toLowerCase())

    if (!matchesSearch) return false

    // State tab filter
    if (filterState.value === 'running') {
      return svc.state === 'Running'
    }
    if (filterState.value === 'issues') {
      return !svc.healthy || svc.state === 'Degraded' || svc.state === 'Stopped'
    }

    return true
  })
})
</script>

<template>
  <div
    v-if="open && node"
    class="fixed inset-0 z-50 flex items-center justify-center p-4 sm:p-6 bg-black/75  animate-fade-in"
    @click.self="emit('close')"
  >
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="services-modal-title"
      class="w-full max-w-3xl max-h-[85vh] flex flex-col rounded-lg bg-zinc-950 border border-zinc-800  overflow-hidden"
    >
      <!-- Header -->
      <div class="px-6 py-4 border-b border-zinc-800/80 bg-zinc-900/60 flex items-center justify-between">
        <div class="flex items-center gap-3">
          <div class="p-2 rounded-lg bg-zinc-800 text-cyan-400 border border-zinc-700/60">
            <Server class="w-5 h-5" />
          </div>
          <div>
            <h2 class="text-sm font-semibold text-zinc-100 flex items-center gap-2">
              <span id="services-modal-title">{{ t('services_title') }}</span>
              <span class="text-xs px-2 py-0.5 rounded bg-zinc-800 text-cyan-300 font-mono">
                {{ node.hostname }} ({{ node.ip }})
              </span>
            </h2>
            <p class="text-xs text-zinc-400">
              {{ t('services_subtitle') }}
            </p>
          </div>
        </div>
        <button
          @click="emit('close')"
          class="p-1.5 rounded-lg text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800 transition-colors cursor-pointer"
          :title="t('close')"
          :aria-label="t('close')"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Controls & Filter Toolbar -->
      <div class="px-6 py-3 border-b border-zinc-800/60 bg-zinc-900/30 flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3">
        <!-- Search Input -->
        <div class="relative flex-1">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-zinc-500" />
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="t('services_search')"
            class="w-full pl-9 pr-3 py-1.5 rounded-lg bg-zinc-900 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-cyan-500/70"
          />
        </div>

        <!-- Filter tabs & Refresh -->
        <div class="flex items-center gap-2">
          <div class="flex items-center rounded-lg bg-zinc-900 border border-zinc-800 p-0.5 text-xs font-medium">
            <button
              @click="filterState = 'all'"
              :class="[
                'px-2.5 py-1 rounded-md transition-all cursor-pointer',
                filterState === 'all'
                  ? 'bg-zinc-800 text-white '
                  : 'text-zinc-400 hover:text-zinc-200',
              ]"
            >
              {{ t('services_filter_all') }} ({{ services.length }})
            </button>
            <button
              @click="filterState = 'running'"
              :class="[
                'px-2.5 py-1 rounded-md transition-all cursor-pointer',
                filterState === 'running'
                  ? 'bg-zinc-800 text-white '
                  : 'text-zinc-400 hover:text-zinc-200',
              ]"
            >
              {{ t('services_filter_running') }}
            </button>
            <button
              @click="filterState = 'issues'"
              :class="[
                'px-2.5 py-1 rounded-md transition-all cursor-pointer',
                filterState === 'issues'
                  ? 'bg-zinc-800 text-rose-300 '
                  : 'text-zinc-400 hover:text-zinc-200',
              ]"
            >
              {{ t('services_filter_issues') }}
            </button>
          </div>

          <button
            @click="loadServices"
            :disabled="loading"
            class="p-1.5 rounded-lg bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 text-zinc-300 transition-colors cursor-pointer"
            :title="t('services_refresh')"
          >
            <RefreshCw :class="['w-4 h-4 text-cyan-400', loading ? 'animate-spin' : '']" />
          </button>
        </div>
      </div>

      <!-- Services Table / List -->
      <div class="flex-1 overflow-y-auto px-6 py-4 divide-y divide-zinc-800/50">
        <div v-if="loading && services.length === 0" class="py-12 text-center text-zinc-500 text-sm">
          <RefreshCw class="w-6 h-6 animate-spin mx-auto mb-2 text-cyan-400" />
          <span>{{ t('services_loading') }}</span>
        </div>

        <div v-else-if="filteredServices.length === 0" class="py-12 text-center text-zinc-500 text-sm">
          <Server class="w-8 h-8 mx-auto mb-2 text-zinc-700" />
          <span>{{ t('services_empty') }}</span>
        </div>

        <div
          v-for="svc in filteredServices"
          :key="svc.id"
          class="py-3 flex items-center justify-between gap-4 hover:bg-zinc-900/40 px-2 rounded-lg transition-colors"
        >
          <!-- Left: Name & Description -->
          <div class="flex items-start gap-3">
            <div
              :class="[
                'mt-1 p-1.5 rounded-md border',
                svc.healthy
                  ? 'bg-emerald-950/40 text-emerald-400 border-emerald-800/40'
                  : 'bg-rose-950/40 text-rose-400 border-rose-800/40',
              ]"
            >
              <CheckCircle2 v-if="svc.healthy" class="w-4 h-4" />
              <AlertCircle v-else class="w-4 h-4" />
            </div>
            <div>
              <div class="flex items-center gap-2">
                <span class="font-mono text-sm font-semibold text-zinc-200">{{ svc.name }}</span>
                <!-- Health pill -->
                <span
                  :class="[
                    'text-[10px] px-1.5 py-0.5 rounded font-medium border',
                    svc.healthy
                      ? 'bg-emerald-950/50 text-emerald-300 border-emerald-800/50'
                      : 'bg-rose-950/50 text-rose-300 border-rose-800/50',
                  ]"
                >
                  {{ svc.healthy ? t('services_health_ok') : t('services_health_fail') }}
                </span>
              </div>
              <p class="text-xs text-zinc-400 mt-0.5">{{ svc.description }}</p>
            </div>
          </div>

          <!-- Right: State, Uptime, Restarts -->
          <div class="flex items-center gap-4 text-xs font-mono">
            <div class="text-right hidden sm:block">
              <span class="text-[10px] text-zinc-500 block uppercase font-sans">{{ t('services_col_uptime') }}</span>
              <span class="text-zinc-300">{{ svc.uptime || '—' }}</span>
            </div>
            <div class="text-right hidden sm:block">
              <span class="text-[10px] text-zinc-500 block uppercase font-sans">{{ t('services_col_restarts') }}</span>
              <span class="text-zinc-400">{{ svc.restarts || 0 }}</span>
            </div>
            <!-- State Badge -->
            <div
              :class="[
                'px-2.5 py-1 rounded-md text-xs font-semibold border flex items-center gap-1.5',
                svc.state === 'Running'
                  ? 'bg-emerald-950/60 text-emerald-400 border-emerald-800/60'
                  : svc.state === 'Waiting'
                  ? 'bg-amber-950/60 text-amber-400 border-amber-800/60'
                  : 'bg-rose-950/60 text-rose-400 border-rose-800/60',
              ]"
            >
              <span
                :class="[
                  'w-1.5 h-1.5 rounded-full',
                  svc.state === 'Running' ? 'bg-emerald-400 ' : 'bg-rose-400',
                ]"
              ></span>
              <span>{{ svc.state }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Footer -->
      <div class="px-6 py-3 border-t border-zinc-800/80 bg-zinc-900/60 flex items-center justify-between">
        <span class="text-xs text-zinc-500">
          Total: {{ services.length }} services monitored
        </span>
        <button
          @click="emit('close')"
          class="px-4 py-1.5 rounded-lg bg-zinc-800 hover:bg-zinc-700 text-xs font-medium text-zinc-200 transition-colors cursor-pointer"
        >
          {{ t('close') }}
        </button>
      </div>
    </div>
  </div>
</template>

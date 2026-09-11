<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import {
  FileCode2,
  Copy,
  Check,
  Download,
  Search,
  RefreshCw,
  Shield,
  Cpu,
  Info,
} from 'lucide-vue-next'
import { t } from '../../i18n'
import type { NodeOverview, MachineConfigData } from '../../types'
import { fetchNodeConfig } from '../../api'

const props = defineProps<{
  nodes: NodeOverview[]
}>()

const selectedNodeIP = ref<string>('')
const loading = ref(false)
const configData = ref<MachineConfigData | null>(null)
const searchQuery = ref('')
const copied = ref(false)

const selectedNode = computed(() => {
  return props.nodes.find((n) => n.ip === selectedNodeIP.value) || props.nodes[0]
})

const loadConfig = async () => {
  if (!selectedNode.value) return
  loading.value = true
  try {
    const isCP = selectedNode.value.role === 'controlplane'
    configData.value = await fetchNodeConfig(
      selectedNode.value.ip,
      selectedNode.value.hostname,
      isCP,
    )
  } catch (err) {
    console.error('Failed to load node config:', err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  if (props.nodes.length > 0) {
    selectedNodeIP.value = props.nodes[0].ip
    loadConfig()
  }
})

watch(
  () => props.nodes,
  (newNodes) => {
    if (!selectedNodeIP.value && newNodes.length > 0) {
      selectedNodeIP.value = newNodes[0].ip
      loadConfig()
    }
  },
  { deep: true },
)

watch(selectedNodeIP, () => {
  loadConfig()
})

// Parsed lines for line numbering & search highlight
const lines = computed(() => {
  if (!configData.value?.configYaml) return []
  return configData.value.configYaml.split('\n')
})

const matchCount = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return 0
  return lines.value.filter((l) => l.toLowerCase().includes(q)).length
})

const copyConfig = async () => {
  if (!configData.value?.configYaml) return
  try {
    await navigator.clipboard.writeText(configData.value.configYaml)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch (err) {
    console.error('Failed to copy config:', err)
  }
}

const downloadConfig = () => {
  if (!configData.value?.configYaml) return
  const blob = new Blob([configData.value.configYaml], { type: 'text/yaml;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${selectedNode.value?.hostname || 'talos'}-machineconfig.yaml`
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

// Quick jump helper: sets search to section keyword
const jumpToSection = (section: string) => {
  searchQuery.value = section
}
</script>

<template>
  <div class="space-y-4">
    <!-- Header & Description -->
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
      <div>
        <div class="flex items-center gap-2.5">
          <h2 class="text-xl font-bold tracking-tight text-zinc-100 flex items-center gap-2">
            <FileCode2 class="w-5 h-5 text-cyan-400" />
            <span>{{ t('config_title') }}</span>
          </h2>
          <span class="px-2.5 py-0.5 rounded-full text-xs font-mono font-semibold bg-zinc-900 text-cyan-300 border border-zinc-800">
            {{ t('config_mode_tag') }}
          </span>
        </div>
        <p class="text-xs text-zinc-400 mt-1">
          {{ t('config_subtitle') }}
        </p>
      </div>

      <!-- Action Buttons: Copy, Download, Refresh -->
      <div class="flex items-center gap-2">
        <button
          @click="copyConfig"
          :disabled="!configData"
          class="flex items-center gap-1.5 px-3 py-2 rounded-xl bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
          :title="t('config_copy')"
        >
          <Check v-if="copied" class="w-3.5 h-3.5 text-emerald-400" />
          <Copy v-else class="w-3.5 h-3.5 text-zinc-400" />
          <span>{{ copied ? t('config_copied') : t('config_copy') }}</span>
        </button>

        <button
          @click="downloadConfig"
          :disabled="!configData"
          class="flex items-center gap-1.5 px-3 py-2 rounded-xl bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
          :title="t('config_download')"
        >
          <Download class="w-3.5 h-3.5 text-cyan-400" />
          <span class="hidden sm:inline">{{ t('config_download') }}</span>
        </button>

        <button
          @click="loadConfig"
          :disabled="loading"
          class="flex items-center gap-1.5 px-3 py-2 rounded-xl bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-xs font-semibold text-zinc-200 transition-all cursor-pointer disabled:opacity-50"
          :title="t('config_refresh')"
        >
          <RefreshCw :class="['w-3.5 h-3.5 text-cyan-400', loading ? 'animate-spin' : '']" />
          <span class="hidden sm:inline">{{ loading ? t('refreshing') : t('config_refresh') }}</span>
        </button>
      </div>
    </div>

    <!-- Node Selector Pill Bar & Search -->
    <div class="flex flex-col lg:flex-row items-stretch lg:items-center justify-between gap-3 p-3 bg-zinc-900/80 border border-zinc-800/80 rounded-2xl backdrop-blur-sm">
      <!-- Node Selector Pills -->
      <div class="flex items-center gap-2 overflow-x-auto pb-1 lg:pb-0">
        <span class="text-xs font-semibold text-zinc-400 whitespace-nowrap pl-1 hidden sm:inline">
          {{ t('config_select_node') }}
        </span>
        <button
          v-for="node in nodes"
          :key="node.ip"
          @click="selectedNodeIP = node.ip"
          :class="[
            'flex items-center gap-2 px-3 py-1.5 rounded-xl text-xs font-mono font-medium transition-all cursor-pointer whitespace-nowrap border',
            selectedNodeIP === node.ip
              ? 'bg-zinc-800 text-cyan-300 border-cyan-500/50 shadow-md shadow-cyan-950/30'
              : 'bg-zinc-950/60 text-zinc-400 hover:text-zinc-200 border-zinc-800/80 hover:border-zinc-700',
          ]"
        >
          <Shield v-if="node.role === 'controlplane'" class="w-3.5 h-3.5 text-violet-400" />
          <Cpu v-else class="w-3.5 h-3.5 text-sky-400" />
          <span class="font-bold">{{ node.hostname }}</span>
          <span class="text-[10px] text-zinc-500">({{ node.ip }})</span>
        </button>
      </div>

      <!-- Search in YAML -->
      <div class="flex items-center gap-2">
        <div class="relative w-full sm:w-64">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-zinc-500" />
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="t('config_search_placeholder')"
            class="w-full pl-8 pr-8 py-1.5 rounded-xl bg-zinc-950 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-cyan-500/70 font-mono"
          />
          <button
            v-if="searchQuery"
            @click="searchQuery = ''"
            class="absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-500 hover:text-zinc-300 text-xs"
          >
            ×
          </button>
        </div>

        <span
          v-if="searchQuery"
          class="px-2 py-1 rounded-lg text-xs font-mono font-semibold bg-cyan-950/80 text-cyan-300 border border-cyan-800/60 whitespace-nowrap"
        >
          {{ matchCount }} {{ t('config_matches') }}
        </span>
      </div>
    </div>

    <!-- Quick Section Navigation Chips -->
    <div class="flex items-center gap-1.5 flex-wrap text-xs text-zinc-400 px-1">
      <span class="text-[11px] text-zinc-500 font-semibold uppercase tracking-wider mr-1">Sections:</span>
      <button
        v-for="sec in ['machine', 'network', 'install', 'kubelet', 'sysctls', 'cluster', 'etcd']"
        :key="sec"
        @click="jumpToSection(sec)"
        class="px-2 py-0.5 rounded-md bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 hover:border-zinc-700 text-zinc-300 font-mono text-[11px] transition-colors cursor-pointer"
      >
        {{ sec }}:
      </button>
    </div>

    <!-- YAML Code Viewer Window -->
    <div class="rounded-2xl border border-zinc-800 bg-zinc-950/90 shadow-2xl overflow-hidden backdrop-blur-md">
      <!-- Window Title Bar -->
      <div class="flex items-center justify-between px-4 py-2.5 bg-zinc-900/90 border-b border-zinc-800 text-xs">
        <div class="flex items-center gap-2">
          <div class="flex items-center gap-1.5 mr-2">
            <span class="w-2.5 h-2.5 rounded-full bg-rose-500/80"></span>
            <span class="w-2.5 h-2.5 rounded-full bg-amber-500/80"></span>
            <span class="w-2.5 h-2.5 rounded-full bg-emerald-500/80"></span>
          </div>
          <span class="font-mono text-zinc-300 font-semibold">
            {{ selectedNode?.hostname || 'talos' }}-config.yaml
          </span>
          <span class="text-zinc-500">({{ lines.length }} {{ t('config_lines') }})</span>
        </div>

        <div class="flex items-center gap-2 text-[11px] text-zinc-500 font-mono">
          <span>syntax: yaml</span>
          <span>•</span>
          <span>encoding: utf-8</span>
        </div>
      </div>

      <!-- Editor Content with Line Numbers -->
      <div class="max-h-[640px] overflow-y-auto p-4 font-mono text-xs leading-relaxed select-text">
        <div
          v-for="(line, idx) in lines"
          :key="idx"
          :class="[
            'flex items-start gap-4 py-0.5 px-2 rounded transition-colors',
            searchQuery && line.toLowerCase().includes(searchQuery.toLowerCase())
              ? 'bg-cyan-950/60 border-l-2 border-cyan-400 text-cyan-200'
              : 'hover:bg-zinc-900/40 text-zinc-300',
          ]"
        >
          <!-- Line Number Gutter -->
          <span class="w-8 text-right text-zinc-400 select-none font-mono text-[11px] shrink-0">
            {{ idx + 1 }}
          </span>

          <!-- Line Content with subtle syntax colouring -->
          <span class="whitespace-pre flex-1 font-mono">
            <template v-if="line.trim().startsWith('#')">
              <span class="text-zinc-500 italic">{{ line }}</span>
            </template>
            <template v-else-if="line.includes(':')">
              <span class="text-cyan-300 font-semibold">{{ line.slice(0, line.indexOf(':') + 1) }}</span>
              <span class="text-emerald-300">{{ line.slice(line.indexOf(':') + 1) }}</span>
            </template>
            <template v-else-if="line.trim().startsWith('-')">
              <span class="text-amber-400">{{ line.slice(0, line.indexOf('-') + 1) }}</span>
              <span class="text-zinc-200">{{ line.slice(line.indexOf('-') + 1) }}</span>
            </template>
            <template v-else>
              <span>{{ line }}</span>
            </template>
          </span>
        </div>
      </div>

      <!-- Security & API status footer -->
      <div class="flex items-center justify-between px-4 py-2.5 bg-zinc-900/70 border-t border-zinc-800 text-[11px] text-zinc-400">
        <div class="flex items-center gap-2">
          <Info class="w-3.5 h-3.5 text-cyan-400 shrink-0" />
          <span>{{ t('config_hint_security') }}</span>
        </div>
        <span class="font-mono text-zinc-500 hidden sm:inline">
          endpoint: /api/nodes/{{ selectedNode?.ip }}/config
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, nextTick, onUnmounted } from 'vue'
import {
  X,
  Play,
  Pause,
  Trash2,
  Download,
  Maximize2,
  Minimize2,
  Search,
  Terminal,
  ArrowDown,
} from 'lucide-vue-next'
import { t } from '../i18n'
import { getAuthToken, getDmesgWsUrl } from '../api'
import type { NodeOverview, DmesgLogLine } from '../types'

const props = defineProps<{
  node: NodeOverview | null
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const logs = ref<DmesgLogLine[]>([])
const isPaused = ref(false)
const autoScroll = ref(true)
const searchQuery = ref('')
const isFullscreen = ref(false)
const connectionStatus = ref<'connected' | 'connecting' | 'disconnected' | 'simulated'>('connecting')

const terminalBodyRef = ref<HTMLDivElement | null>(null)
let socket: WebSocket | null = null
let simulationTimer: ReturnType<typeof setInterval> | null = null
let lineIdCounter = 0
let connectionGeneration = 0

// Sample simulated Talos kernel messages for fallback
const SIMULATED_DMESG_TEMPLATES = [
  '[   10.231405] talos: system service etcd is healthy and ready',
  '[   10.514092] kubelet: pod network initialized (cilium/flannel)',
  '[   11.002450] containerd: loaded image registry.k8s.io/kube-apiserver:v1.32.2',
  '[   12.189421] talos: node readiness check passed',
  '[   13.441920] apid: client connection from 10.42.0.1 (mTLS verified, subject=talos-operator)',
  '[   14.891230] kernel: TCP: request_sock_TCP: Possible SYN flooding on port 6443? Sending cookies.',
  '[   16.012903] machined: sync state: current phase "running", ready: true',
  '[   18.449102] kubelet: syncLoop (PLEG): re-listing scheduled',
  '[   21.320491] udevd: event processed for subsystem=net interface=eth0',
  '[   25.992104] timed: NTP synchronized with time.cloudflare.com (offset -0.0012s)',
  '[   30.124502] talos: controller runtime status: healthy',
]

const parseLogLevel = (msg: string): DmesgLogLine['level'] => {
  const lower = msg.toLowerCase()
  if (lower.includes('err') || lower.includes('fail') || lower.includes('panic') || lower.includes('fatal')) return 'error'
  if (lower.includes('warn') || lower.includes('flood') || lower.includes('degraded')) return 'warn'
  if (lower.includes('kernel')) return 'kern'
  if (lower.includes('debug')) return 'debug'
  return 'info'
}

const addLogLine = (raw: string) => {
  if (isPaused.value) return
  const now = new Date()
  const timeStr = `${now.getHours().toString().padStart(2, '0')}:${now.getMinutes().toString().padStart(2, '0')}:${now.getSeconds().toString().padStart(2, '0')}.${now.getMilliseconds().toString().padStart(3, '0')}`

  logs.value.push({
    id: `log-${++lineIdCounter}`,
    timestamp: timeStr,
    raw,
    level: parseLogLevel(raw),
  })

  // Limit buffer to 1000 lines
  if (logs.value.length > 1000) {
    logs.value.shift()
  }

  if (autoScroll.value) {
    nextTick(() => {
      scrollToBottom()
    })
  }
}

const scrollToBottom = () => {
  if (terminalBodyRef.value) {
    terminalBodyRef.value.scrollTop = terminalBodyRef.value.scrollHeight
  }
}

const startSimulation = () => {
  connectionStatus.value = 'simulated'
  if (simulationTimer) clearInterval(simulationTimer)

  // Seed initial lines
  if (logs.value.length === 0) {
    addLogLine('[    0.000000] Linux version 6.6.71-talos (root@siderolabs) (gcc 13.2.0) #1 SMP PREEMPT_DYNAMIC')
    addLogLine('[    0.000001] Command line: talos.platform=metal console=tty0 init_on_alloc=1 slab_nomerge')
    addLogLine('[    1.120300] talos: initializing trust store from /system/secrets/pki')
    addLogLine('[    2.450129] talos: mounting ephemeral overlay filesystem')
    addLogLine('[    3.892019] machined: booting Talos Linux v1.14.0')
    addLogLine(`[    5.120401] networkd: link up eth0 ip=${props.node?.ip || '10.42.0.110'}/24`)
    addLogLine('[    7.340120] apid: listening on 0.0.0.0:50000 (gRPC TLS)')
  }

  simulationTimer = setInterval(() => {
    const randomMsg = SIMULATED_DMESG_TEMPLATES[Math.floor(Math.random() * SIMULATED_DMESG_TEMPLATES.length)]
    addLogLine(randomMsg)
  }, 2500)
}

const connectWebSocket = () => {
  if (!props.node) return
  closeWebSocket()
	const generation = ++connectionGeneration

  connectionStatus.value = 'connecting'
  const wsUrl = getDmesgWsUrl(props.node.ip)

  try {
	const token = getAuthToken()
	socket = token ? new WebSocket(wsUrl, token) : new WebSocket(wsUrl)

    const connectTimeout = setTimeout(() => {
	  if (generation === connectionGeneration && props.open && socket && socket.readyState !== WebSocket.OPEN) {
        console.warn('WS timeout, starting simulated fallback stream')
        socket.close()
        startSimulation()
      }
    }, 2000)

    socket.onopen = () => {
	  if (generation !== connectionGeneration || !props.open) return
      clearTimeout(connectTimeout)
      connectionStatus.value = 'connected'
      if (simulationTimer) clearInterval(simulationTimer)
    }

    socket.onmessage = (event) => {
	  if (generation !== connectionGeneration || !props.open) return
      addLogLine(event.data)
    }

    socket.onerror = (err) => {
	  if (generation !== connectionGeneration || !props.open) return
      console.warn('WS connection error, falling back to simulated stream:', err)
      clearTimeout(connectTimeout)
      startSimulation()
    }

    socket.onclose = () => {
	  if (generation !== connectionGeneration) return
      if (connectionStatus.value === 'connected') {
        connectionStatus.value = 'disconnected'
      }
    }
  } catch (err) {
    console.warn('Failed to open WS, falling back:', err)
    startSimulation()
  }
}

const closeWebSocket = () => {
	connectionGeneration++
  if (socket) {
    socket.close()
    socket = null
  }
  if (simulationTimer) {
    clearInterval(simulationTimer)
    simulationTimer = null
  }
}

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen && props.node) {
      logs.value = []
      lineIdCounter = 0
      isPaused.value = false
      autoScroll.value = true
      searchQuery.value = ''
      connectWebSocket()
    } else {
      closeWebSocket()
    }
  },
  { immediate: true }
)

onUnmounted(() => {
  closeWebSocket()
})

const clearLogs = () => {
  logs.value = []
}

const togglePause = () => {
  isPaused.value = !isPaused.value
}

const toggleFullscreen = () => {
  isFullscreen.value = !isFullscreen.value
}

const exportLogs = () => {
  if (!props.node) return
  const content = logs.value.map((l) => `[${l.timestamp}] ${l.raw}`).join('\n')
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `dmesg-${props.node.hostname}-${Date.now()}.log`
  a.click()
  URL.revokeObjectURL(url)
}

const filteredLogs = computed(() => {
  if (!searchQuery.value.trim()) return logs.value
  const q = searchQuery.value.toLowerCase()
  return logs.value.filter((l) => l.raw.toLowerCase().includes(q))
})
</script>

<template>
  <div
    v-if="open && node"
    class="fixed inset-0 z-50 flex items-center justify-center p-2 sm:p-4 bg-black/80 backdrop-blur-md animate-fade-in"
    @click.self="emit('close')"
  >
    <div
      :class="[
        'w-full flex flex-col rounded-2xl bg-zinc-950 border border-zinc-800 shadow-2xl shadow-black overflow-hidden font-mono transition-all duration-300',
        isFullscreen ? 'h-full max-w-full rounded-none' : 'max-w-5xl h-[85vh]',
      ]"
    >
      <!-- Title Bar -->
      <div class="px-5 py-3 border-b border-zinc-800/80 bg-zinc-900/70 flex items-center justify-between font-sans">
        <div class="flex items-center gap-3">
          <div class="p-2 rounded-lg bg-zinc-800 text-emerald-400 border border-zinc-700/60">
            <Terminal class="w-4 h-4" />
          </div>
          <div>
            <div class="flex items-center gap-2">
              <h2 class="text-sm font-bold text-zinc-100">{{ t('logs_title') }}</h2>
              <span class="text-xs px-2 py-0.5 rounded bg-zinc-800 text-cyan-300 font-mono">
                {{ node.hostname }} ({{ node.ip }})
              </span>
            </div>
            <!-- Connection status badge -->
            <div class="flex items-center gap-2 mt-0.5 text-[11px]">
              <span class="flex items-center gap-1.5 font-medium">
                <span
                  :class="[
                    'w-2 h-2 rounded-full',
                    connectionStatus === 'connected'
                      ? 'bg-emerald-400 animate-pulse'
                      : connectionStatus === 'simulated'
                      ? 'bg-cyan-400'
                      : connectionStatus === 'connecting'
                      ? 'bg-amber-400 animate-pulse'
                      : 'bg-rose-500',
                  ]"
                ></span>
                <span
                  :class="[
                    connectionStatus === 'connected'
                      ? 'text-emerald-400'
                      : connectionStatus === 'simulated'
                      ? 'text-cyan-400'
                      : connectionStatus === 'connecting'
                      ? 'text-amber-400'
                      : 'text-rose-400',
                  ]"
                >
                  {{
                    connectionStatus === 'connected'
                      ? t('logs_connected')
                      : connectionStatus === 'simulated'
                      ? t('logs_simulated')
                      : connectionStatus === 'connecting'
                      ? t('logs_connecting')
                      : t('logs_disconnected')
                  }}
                </span>
              </span>
              <span class="text-zinc-600">•</span>
              <span class="text-zinc-400">{{ logs.length }} {{ t('logs_lines_count') }}</span>
            </div>
          </div>
        </div>

        <!-- Right: Window buttons -->
        <div class="flex items-center gap-1.5">
          <button
            @click="toggleFullscreen"
            class="p-1.5 rounded-lg text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800 transition-colors cursor-pointer"
            :title="isFullscreen ? t('logs_exit_fullscreen') : t('logs_fullscreen')"
          >
            <Minimize2 v-if="isFullscreen" class="w-4 h-4" />
            <Maximize2 v-else class="w-4 h-4" />
          </button>
          <button
            @click="emit('close')"
            class="p-1.5 rounded-lg text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800 transition-colors cursor-pointer"
            :title="t('close')"
          >
            <X class="w-5 h-5" />
          </button>
        </div>
      </div>

      <!-- Controls Bar -->
      <div class="px-5 py-2.5 border-b border-zinc-800/60 bg-zinc-900/40 flex flex-wrap items-center justify-between gap-2.5 font-sans">
        <!-- Search -->
        <div class="relative flex-1 min-w-[200px] max-w-md">
          <Search class="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-zinc-500" />
          <input
            v-model="searchQuery"
            type="text"
            :placeholder="t('logs_search')"
            class="w-full pl-8 pr-3 py-1 rounded bg-zinc-950 border border-zinc-800 text-xs text-zinc-200 placeholder-zinc-500 focus:outline-none focus:border-cyan-500"
          />
        </div>

        <!-- Buttons group -->
        <div class="flex items-center gap-1.5 text-xs">
          <!-- Pause / Resume -->
          <button
            @click="togglePause"
            :class="[
              'flex items-center gap-1.5 px-2.5 py-1 rounded border transition-all cursor-pointer font-medium',
              isPaused
                ? 'bg-amber-950/70 text-amber-300 border-amber-800/80 hover:bg-amber-900/80'
                : 'bg-zinc-800 hover:bg-zinc-700 text-zinc-200 border-zinc-700',
            ]"
          >
            <Play v-if="isPaused" class="w-3.5 h-3.5 text-amber-400 fill-amber-400" />
            <Pause v-else class="w-3.5 h-3.5 text-cyan-400" />
            <span>{{ isPaused ? t('logs_resume') : t('logs_pause') }}</span>
          </button>

          <!-- Auto-scroll toggle -->
          <button
            @click="autoScroll = !autoScroll"
            :class="[
              'flex items-center gap-1.5 px-2.5 py-1 rounded border transition-all cursor-pointer font-medium',
              autoScroll
                ? 'bg-cyan-950/70 text-cyan-300 border-cyan-800/80 hover:bg-cyan-900/80'
                : 'bg-zinc-800 hover:bg-zinc-700 text-zinc-400 border-zinc-700',
            ]"
          >
            <ArrowDown class="w-3.5 h-3.5" />
            <span>{{ t('logs_autoscroll') }}</span>
          </button>

          <!-- Clear -->
          <button
            @click="clearLogs"
            class="flex items-center gap-1.5 px-2.5 py-1 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-300 border border-zinc-700 transition-all cursor-pointer font-medium"
            :title="t('logs_clear')"
          >
            <Trash2 class="w-3.5 h-3.5 text-rose-400" />
            <span>{{ t('logs_clear') }}</span>
          </button>

          <!-- Export -->
          <button
            @click="exportLogs"
            class="flex items-center gap-1.5 px-2.5 py-1 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-300 border border-zinc-700 transition-all cursor-pointer font-medium"
            :title="t('logs_export')"
          >
            <Download class="w-3.5 h-3.5 text-zinc-400" />
            <span>{{ t('logs_export') }}</span>
          </button>
        </div>
      </div>

      <!-- Terminal Screen -->
      <div
        ref="terminalBodyRef"
        class="flex-1 bg-black/95 p-4 overflow-y-auto text-[12px] leading-relaxed select-text font-mono border-zinc-800"
      >
        <div v-if="filteredLogs.length === 0" class="h-full flex items-center justify-center text-zinc-600 font-sans text-xs">
          <span>{{ t('logs_empty') }}</span>
        </div>

        <div
          v-for="line in filteredLogs"
          :key="line.id"
          class="hover:bg-zinc-900/50 px-1.5 py-0.5 rounded flex items-start gap-2.5 group"
        >
          <!-- Timestamp -->
          <span class="text-zinc-600 select-none text-[11px] shrink-0 font-mono">
            {{ line.timestamp }}
          </span>

          <!-- Log Content -->
          <span
            :class="[
              'break-all font-mono',
              line.level === 'error'
                ? 'text-rose-400 font-semibold'
                : line.level === 'warn'
                ? 'text-amber-300'
                : line.level === 'kern'
                ? 'text-cyan-300'
                : 'text-zinc-300',
            ]"
          >
            {{ line.raw }}
          </span>
        </div>
      </div>

      <!-- Terminal Footer Bar -->
      <div class="px-5 py-2 border-t border-zinc-800/80 bg-zinc-900/80 flex items-center justify-between text-xs text-zinc-500 font-sans">
        <div class="flex items-center gap-3">
          <span class="font-mono text-[11px] text-zinc-400">
            WS: /ws/nodes/{{ node.ip }}/dmesg
          </span>
          <span v-if="searchQuery" class="text-cyan-400 text-[11px]">
            Filtered: {{ filteredLogs.length }} of {{ logs.length }} lines
          </span>
        </div>
        <button
          @click="scrollToBottom"
          class="flex items-center gap-1 text-[11px] text-zinc-400 hover:text-cyan-400 cursor-pointer"
        >
          <ArrowDown class="w-3 h-3" />
          <span>Scroll to bottom</span>
        </button>
      </div>
    </div>
  </div>
</template>

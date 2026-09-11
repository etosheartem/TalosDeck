<script setup lang="ts">
import { ref } from 'vue'
import {
  Shield,
  Cpu,
  Copy,
  Check,
  Server,
  Terminal,
  RotateCw,
  Clock,
  Activity,
} from 'lucide-vue-next'
import { t } from '../i18n'
import type { NodeOverview } from '../types'

const props = defineProps<{
  node: NodeOverview
}>()

const emit = defineEmits<{
  (e: 'open-services', node: NodeOverview): void
  (e: 'open-logs', node: NodeOverview): void
  (e: 'open-reboot', node: NodeOverview): void
}>()

const copied = ref(false)

const copyIp = async () => {
  try {
    await navigator.clipboard.writeText(props.node.ip)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch (err) {
    console.error('Failed to copy IP', err)
  }
}

const getServiceStatus = (serviceName: 'etcd' | 'kubelet' | 'containerd' | 'apid'): { color: string; label: string } => {
  const summary = props.node.servicesSummary
  const raw = summary ? summary[serviceName] : undefined
  if (raw === 'Healthy') {
    return { color: 'bg-emerald-400 shadow-[0_0_6px_rgba(52,211,153,0.6)]', label: 'Healthy' }
  }
  if (raw === 'Degraded') {
    return { color: 'bg-rose-500 animate-pulse shadow-[0_0_6px_rgba(244,63,94,0.6)]', label: 'Degraded' }
  }
  if (raw === 'N/A') {
    return { color: 'bg-zinc-600', label: 'N/A' }
  }
  if (props.node.ready) {
    return { color: 'bg-emerald-400', label: 'Healthy' }
  }
  return { color: 'bg-rose-500', label: 'Degraded' }
}
</script>

<template>
  <div
    :class="[
      'group relative flex flex-col justify-between rounded-2xl bg-zinc-900/80 border p-5 transition-all duration-200 backdrop-blur-sm',
      node.ready
        ? 'border-zinc-800/90 hover:border-cyan-500/50 hover:shadow-xl hover:shadow-cyan-950/20'
        : 'border-rose-900/60 bg-rose-950/10 hover:border-rose-700/60 shadow-lg shadow-rose-950/20',
    ]"
  >
    <!-- Top Row: Hostname & Role + Status -->
    <div>
      <div class="flex items-start justify-between gap-3 mb-3">
        <div>
          <div class="flex items-center gap-2">
            <h3 class="text-base font-bold text-zinc-100 tracking-tight font-mono group-hover:text-cyan-300 transition-colors">
              {{ node.hostname }}
            </h3>
          </div>
          <!-- IP with Copy Button -->
          <div class="flex items-center gap-1.5 mt-1">
            <span class="font-mono text-xs text-zinc-400 select-all">{{ node.ip }}</span>
            <button
              @click="copyIp"
              class="p-1 text-zinc-500 hover:text-cyan-400 rounded transition-colors cursor-pointer"
              :title="copied ? t('node_copied') : t('node_copy_ip')"
              :aria-label="copied ? t('node_copied') : t('node_copy_ip')"
            >
              <Check v-if="copied" class="w-3.5 h-3.5 text-emerald-400" />
              <Copy v-else class="w-3.5 h-3.5" />
            </button>
            <span v-if="copied" class="text-[10px] text-emerald-400 font-medium animate-fade-in">
              {{ t('node_copied') }}
            </span>
          </div>
        </div>

        <!-- Badges: Role and Ready -->
        <div class="flex flex-col items-end gap-1.5">
          <!-- Ready Badge with Glowing Dot -->
          <div
            :class="[
              'inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-semibold tracking-wide border',
              node.ready
                ? 'bg-emerald-950/80 text-emerald-300 border-emerald-800/80 shadow-[0_0_12px_rgba(16,185,129,0.3)]'
                : 'bg-rose-950/80 text-rose-300 border-rose-800/80 shadow-[0_0_12px_rgba(244,63,94,0.3)]',
            ]"
          >
            <span class="relative flex h-2 w-2">
              <span
                v-if="node.ready"
                class="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"
              ></span>
              <span
                :class="[
                  'relative inline-flex rounded-full h-2 w-2',
                  node.ready ? 'bg-emerald-400' : 'bg-rose-500',
                ]"
              ></span>
            </span>
            <span>{{ node.ready ? t('node_status_ready') : t('node_status_not_ready') }}</span>
          </div>

          <!-- Role Badge -->
          <div
            :class="[
              'inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[11px] font-medium tracking-tight border',
              node.role === 'controlplane'
                ? 'bg-violet-950/50 text-violet-300 border-violet-800/50'
                : 'bg-sky-950/50 text-sky-300 border-sky-800/50',
            ]"
          >
            <Shield v-if="node.role === 'controlplane'" class="w-3 h-3" />
            <Cpu v-else class="w-3 h-3" />
            <span>{{ node.role === 'controlplane' ? t('node_role_cp') : t('node_role_worker') }}</span>
          </div>
        </div>
      </div>

      <!-- Specs & Metadata Grid -->
      <div class="grid grid-cols-2 gap-2 my-3.5 p-3 rounded-xl bg-zinc-950/60 border border-zinc-800/60 text-xs">
        <div>
          <span class="text-zinc-500 block text-[11px]">{{ t('node_version') }}</span>
          <span class="font-mono text-zinc-200 font-semibold">{{ node.version || 'v1.14.0' }}</span>
        </div>
        <div>
          <span class="text-zinc-500 block text-[11px]">{{ t('node_k8s_version') }}</span>
          <span class="font-mono text-emerald-400 font-semibold">{{ node.kubernetesVersion || 'v1.32.2' }}</span>
        </div>
        <div class="col-span-2 pt-1 border-t border-zinc-800/40 flex items-center justify-between text-[11px] text-zinc-400">
          <div class="flex items-center gap-1.5">
            <Clock class="w-3 h-3 text-zinc-500" />
            <span>{{ t('node_uptime') }}:</span>
            <span class="text-zinc-300">{{ node.uptime || '14 days' }}</span>
          </div>
          <div v-if="node.memoryUsage" class="flex items-center gap-1 font-mono text-[10px] text-zinc-400">
            <Activity class="w-3 h-3 text-cyan-400" />
            <span>RAM {{ node.memoryUsage }}</span>
          </div>
        </div>
      </div>

      <!-- Key Services Status Pills -->
      <div class="mb-4">
        <div class="flex items-center justify-between text-[11px] text-zinc-400 mb-1.5">
          <span class="font-medium">{{ t('node_services_summary') }}</span>
          <span class="text-[10px] text-zinc-500">Talos Machine Services</span>
        </div>
        <div class="flex flex-wrap gap-1.5">
          <!-- etcd (if controlplane) -->
          <div
            v-if="node.role === 'controlplane'"
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded bg-zinc-950 border border-zinc-800 text-[11px] font-mono text-zinc-300"
            :title="`etcd: ${getServiceStatus('etcd').label}`"
          >
            <span :class="['w-1.5 h-1.5 rounded-full', getServiceStatus('etcd').color]"></span>
            <span>etcd</span>
          </div>
          <!-- kubelet -->
          <div
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded bg-zinc-950 border border-zinc-800 text-[11px] font-mono text-zinc-300"
            :title="`kubelet: ${getServiceStatus('kubelet').label}`"
          >
            <span :class="['w-1.5 h-1.5 rounded-full', getServiceStatus('kubelet').color]"></span>
            <span>kubelet</span>
          </div>
          <!-- containerd -->
          <div
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded bg-zinc-950 border border-zinc-800 text-[11px] font-mono text-zinc-300"
            :title="`containerd: ${getServiceStatus('containerd').label}`"
          >
            <span :class="['w-1.5 h-1.5 rounded-full', getServiceStatus('containerd').color]"></span>
            <span>containerd</span>
          </div>
          <!-- apid -->
          <div
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded bg-zinc-950 border border-zinc-800 text-[11px] font-mono text-zinc-300"
            :title="`apid: ${getServiceStatus('apid').label}`"
          >
            <span :class="['w-1.5 h-1.5 rounded-full', getServiceStatus('apid').color]"></span>
            <span>apid</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Action Buttons -->
    <div class="pt-3 border-t border-zinc-800/80 grid grid-cols-3 gap-2">
      <!-- Services Button -->
      <button
        @click="emit('open-services', node)"
        class="min-w-0 flex items-center justify-center gap-1.5 px-2.5 py-1.5 rounded-lg bg-zinc-800/70 hover:bg-zinc-800 border border-zinc-700/60 hover:border-zinc-600 text-xs font-medium text-zinc-200 hover:text-white transition-all cursor-pointer shadow-sm active:scale-95"
      >
        <Server class="w-3.5 h-3.5 text-cyan-400 shrink-0" />
        <span class="truncate">{{ t('node_btn_services') }}</span>
      </button>

      <!-- Live Logs Button -->
      <button
        @click="emit('open-logs', node)"
        class="min-w-0 flex items-center justify-center gap-1.5 px-2.5 py-1.5 rounded-lg bg-zinc-800/70 hover:bg-zinc-800 border border-zinc-700/60 hover:border-zinc-600 text-xs font-medium text-zinc-200 hover:text-white transition-all cursor-pointer shadow-sm active:scale-95"
      >
        <Terminal class="w-3.5 h-3.5 text-emerald-400 shrink-0" />
        <span class="truncate">{{ t('node_btn_logs') }}</span>
      </button>

      <!-- Reboot Button -->
      <button
        @click="emit('open-reboot', node)"
        class="min-w-0 flex items-center justify-center gap-1.5 px-2.5 py-1.5 rounded-lg bg-zinc-800/70 hover:bg-rose-950/60 border border-zinc-700/60 hover:border-rose-700/60 text-xs font-medium text-zinc-200 hover:text-rose-300 transition-all cursor-pointer shadow-sm active:scale-95 group/btn"
      >
        <RotateCw class="w-3.5 h-3.5 text-zinc-400 group-hover/btn:text-rose-400 group-hover/btn:rotate-180 transition-transform duration-300 shrink-0" />
        <span class="truncate">{{ t('node_btn_reboot') }}</span>
      </button>
    </div>
  </div>
</template>

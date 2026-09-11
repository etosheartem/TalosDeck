<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import {
  X,
  Server,
  Cpu,
  HardDrive,
  Activity,
  Layers,
  CheckCircle2,
  AlertCircle,
  RotateCw,
  Clock,
  Sparkles,
  Zap,
} from 'lucide-vue-next'
import { t } from '../i18n'
import { fetchProxmoxStatus, fetchNextVMID, createProxmoxWorker } from '../api'
import type { ProxmoxStatusResponse, CreateWorkerResult } from '../types'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'success', result: CreateWorkerResult): void
  (e: 'error', error: string): void
}>()

// Form state
const vmid = ref<number>(113)
const name = ref<string>('talos-worker-3')
const cores = ref<number>(2)
const memoryGB = ref<number>(3)
const diskGB = ref<number>(30)
const role = ref<'worker'>('worker')
const storage = ref<string>('local-lvm')
const bridge = ref<string>('vmbr0')
const autoStart = ref<boolean>(true)

// RFC 1123 node name validation
const RFC1123_REGEX = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
const isNameValid = computed(() => {
  const val = name.value
  return val.length >= 1 && val.length <= 63 && RFC1123_REGEX.test(val)
})

// Host status state
const proxmoxStatus = ref<ProxmoxStatusResponse | null>(null)
const loadingProxmox = ref<boolean>(false)
const loadingVMID = ref<boolean>(false)

// Creation process state
const isCreating = ref<boolean>(false)
const currentStep = ref<number>(1)
const elapsedSeconds = ref<number>(0)
const errorMessage = ref<string>('')
let timerInterval: ReturnType<typeof setInterval> | null = null
let stepInterval: ReturnType<typeof setInterval> | null = null

// Form presets
const cpuPresets = [1, 2, 3, 4]
const ramPresets = [2, 3, 4, 6, 8]
const diskPresets = [20, 30, 50, 80, 100]

// Proxmox resource calculations
const hostMemoryFreeGB = computed(() => {
  if (!proxmoxStatus.value?.status?.memory) return 16.0
  const freeBytes = proxmoxStatus.value.status.memory.free ?? proxmoxStatus.value.status.memory.available ?? 0
  return Number((freeBytes / (1024 * 1024 * 1024)).toFixed(1))
})

const hostMemoryTotalGB = computed(() => {
  if (!proxmoxStatus.value?.status?.memory) return 32.0
  return Number((proxmoxStatus.value.status.memory.total / (1024 * 1024 * 1024)).toFixed(1))
})

const hostMemoryUsedPercent = computed(() => {
  return Math.round(proxmoxStatus.value?.status?.memory?.usagePercent ?? 50)
})

const hostDiskFreeGB = computed(() => {
  if (!proxmoxStatus.value?.status?.storage) return 358.4
  return Number((proxmoxStatus.value.status.storage.free / (1024 * 1024 * 1024)).toFixed(1))
})

const hostDiskTotalGB = computed(() => {
  if (!proxmoxStatus.value?.status?.storage) return 512.0
  return Number((proxmoxStatus.value.status.storage.total / (1024 * 1024 * 1024)).toFixed(1))
})

const hostDiskUsedPercent = computed(() => {
  return Math.round(proxmoxStatus.value?.status?.storage?.usagePercent ?? 30)
})

const hostCpuUsage = computed(() => {
  return Number((proxmoxStatus.value?.status?.cpuUsagePercent ?? 12.4).toFixed(1))
})

const hostCpuCores = computed(() => {
  return proxmoxStatus.value?.status?.cpuCores ?? 8
})

const isHostConfigured = computed(() => {
  return proxmoxStatus.value?.configured ?? true
})

// Load Proxmox node status and next VMID
const loadData = async () => {
  loadingProxmox.value = true
  errorMessage.value = ''
  try {
    const status = await fetchProxmoxStatus()
    proxmoxStatus.value = status
  } catch (e: any) {
    console.error('Failed to load Proxmox status:', e)
  } finally {
    loadingProxmox.value = false
  }

  await refreshVMID()
}

const refreshVMID = async () => {
  loadingVMID.value = true
  try {
    const nextId = await fetchNextVMID()
    vmid.value = nextId
    // Suggest worker name based on VMID (e.g. 113 -> talos-worker-3)
    const workerNum = nextId >= 110 ? nextId - 110 : nextId
    name.value = `talos-worker-${workerNum}`
  } catch (e: any) {
    console.error('Failed to get next VMID:', e)
  } finally {
    loadingVMID.value = false
  }
}

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) {
      errorMessage.value = ''
      isCreating.value = false
      currentStep.value = 1
      elapsedSeconds.value = 0
      loadData()
    } else {
      stopTimers()
    }
  },
  { immediate: true }
)

const stopTimers = () => {
  if (timerInterval) {
    clearInterval(timerInterval)
    timerInterval = null
  }
  if (stepInterval) {
    clearInterval(stepInterval)
    stepInterval = null
  }
}

onUnmounted(() => {
  stopTimers()
})

const formatElapsed = (sec: number): string => {
  const m = Math.floor(sec / 60).toString().padStart(2, '0')
  const s = (sec % 60).toString().padStart(2, '0')
  return `${m}:${s}`
}

const handleSubmit = async () => {
  errorMessage.value = ''
  if (!name.value.trim()) {
    errorMessage.value = 'Пожалуйста, введите имя ноды'
    return
  }
  if (!isNameValid.value) {
    errorMessage.value = 'Имя ноды должно соответствовать RFC 1123 (строчные буквы a-z, цифры 0-9, дефис, длина 1-63)'
    return
  }
  if (!Number.isInteger(vmid.value) || vmid.value < 100 || vmid.value > 9999) {
    errorMessage.value = 'VMID должен быть целым числом от 100 до 9999'
    return
  }

  isCreating.value = true
  currentStep.value = 1
  elapsedSeconds.value = 0

  timerInterval = setInterval(() => {
    elapsedSeconds.value++
  }, 1000)

  stepInterval = setInterval(() => {
    if (currentStep.value < 4) {
      currentStep.value++
    }
  }, 3500)

  try {
    const result = await createProxmoxWorker({
      vmid: vmid.value,
      name: name.value.trim(),
      cores: cores.value,
      memoryMB: memoryGB.value * 1024,
      diskGB: diskGB.value,
      storage: storage.value,
      bridge: bridge.value,
      start: autoStart.value,
    })

    stopTimers()
    emit('success', result)
    emit('close')
  } catch (err: any) {
    stopTimers()
    isCreating.value = false
    errorMessage.value = err.message || t('add_worker_error')
    emit('error', errorMessage.value)
  }
}
</script>

<template>
  <div
    v-if="open"
    class="fixed inset-0 z-50 flex items-center justify-center p-3 sm:p-4 bg-black/80  overflow-y-auto animate-fade-in"
    @click.self="!isCreating && emit('close')"
  >
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="add-worker-modal-title"
      class="w-full max-w-2xl rounded-lg bg-zinc-950 border border-zinc-800  shadow-cyan-950/20 overflow-hidden flex flex-col max-h-[92vh] my-auto"
    >
      <!-- Modal Header -->
      <div class="px-6 py-4.5 border-b border-zinc-800/80 bg-zinc-900/40 flex items-center justify-between shrink-0">
        <div class="flex items-center gap-3">
          <div class="w-9 h-9 rounded-md bg-cyan-500/10 border border-cyan-500/30 flex items-center justify-center text-cyan-400 shadow-inner">
            <Server class="w-5 h-5" />
          </div>
          <div>
            <div class="flex items-center gap-2">
              <h2 id="add-worker-modal-title" class="text-sm font-semibold text-zinc-100 tracking-tight">
                {{ t('add_worker_title') }}
              </h2>
              <span class="px-2 py-0.5 rounded-full text-[10px] font-semibold bg-cyan-950 border border-cyan-700/60 text-cyan-300">
                Proxmox VE
              </span>
            </div>
            <p class="text-xs text-zinc-400 mt-0.5">
              {{ t('add_worker_subtitle') }}
            </p>
          </div>
        </div>

        <button
          v-if="!isCreating"
          @click="emit('close')"
          class="text-zinc-500 hover:text-zinc-300 p-1.5 rounded-lg hover:bg-zinc-800 transition-colors cursor-pointer"
          :title="t('close')"
          :aria-label="t('close')"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Modal Body (Scrollable) -->
      <div class="p-6 space-y-4 overflow-y-auto custom-scrollbar flex-1">
        <!-- Error Banner if Any -->
        <div
          v-if="errorMessage"
          class="p-3.5 rounded-md bg-rose-950/40 border border-rose-800/60 text-rose-300 text-xs flex items-start gap-2.5 animate-shake"
        >
          <AlertCircle class="w-4 h-4 shrink-0 mt-0.5 text-rose-400" />
          <div class="flex-1">
            <p class="font-semibold">{{ t('add_worker_error') }}</p>
            <p class="mt-0.5 opacity-90 text-[11px]">{{ errorMessage }}</p>
          </div>
        </div>

        <!-- 1. Proxmox VE Host Live Resources -->
        <div class="rounded-md bg-zinc-900/60 border border-zinc-800/80 p-4 space-y-3">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2">
              <Activity class="w-4 h-4 text-cyan-400" />
              <span class="text-xs font-semibold text-zinc-200">
                {{ t('add_worker_host_resources') }}
              </span>
              <span class="font-mono text-[11px] text-zinc-400">
                ({{ proxmoxStatus?.node || 'pve' }})
              </span>
            </div>

            <div class="flex items-center gap-2">
              <span
                :class="[
                  'inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10px] font-medium border',
                  isHostConfigured
                    ? 'bg-emerald-950/60 border-emerald-800/60 text-emerald-400'
                    : 'bg-amber-950/60 border-amber-800/60 text-amber-400',
                ]"
              >
                <span :class="['w-1.5 h-1.5 rounded-full', isHostConfigured ? 'bg-emerald-400 ' : 'bg-amber-400']" />
                {{ isHostConfigured ? t('proxmox_status_connected') : t('proxmox_status_unconfigured') }}
              </span>
              <button
                type="button"
                @click="loadData"
                :disabled="loadingProxmox || isCreating"
                class="p-1 text-zinc-400 hover:text-cyan-400 transition-colors disabled:opacity-40 cursor-pointer"
                :title="t('refresh')"
              >
                <RotateCw :class="['w-3.5 h-3.5', loadingProxmox ? 'animate-spin' : '']" />
              </button>
            </div>
          </div>

          <!-- 3 Resource Gauges Grid -->
          <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 pt-1">
            <!-- RAM Card -->
            <div class="p-3 rounded-lg bg-zinc-950/70 border border-zinc-800/60 space-y-2">
              <div class="flex items-center justify-between text-[11px]">
                <span class="text-zinc-400 flex items-center gap-1.5">
                  <Layers class="w-3.5 h-3.5 text-indigo-400" />
                  {{ t('proxmox_free_ram') }}
                </span>
                <span class="font-mono font-bold text-indigo-300">
                  {{ hostMemoryFreeGB }} GB
                </span>
              </div>
              <div class="w-full bg-zinc-800 h-1.5 rounded-full overflow-hidden">
                <div
                  class="bg-gradient-to-r from-indigo-500 to-cyan-500 h-full rounded-full transition-all duration-500"
                  :style="{ width: `${hostMemoryUsedPercent}%` }"
                />
              </div>
              <div class="flex justify-between text-[10px] text-zinc-500 font-mono">
                <span>{{ hostMemoryUsedPercent }}% used</span>
                <span>{{ hostMemoryTotalGB }} GB total</span>
              </div>
            </div>

            <!-- Disk Card -->
            <div class="p-3 rounded-lg bg-zinc-950/70 border border-zinc-800/60 space-y-2">
              <div class="flex items-center justify-between text-[11px]">
                <span class="text-zinc-400 flex items-center gap-1.5">
                  <HardDrive class="w-3.5 h-3.5 text-fuchsia-400" />
                  {{ t('proxmox_free_disk') }}
                </span>
                <span class="font-mono font-bold text-fuchsia-300">
                  {{ hostDiskFreeGB }} GB
                </span>
              </div>
              <div class="w-full bg-zinc-800 h-1.5 rounded-full overflow-hidden">
                <div
                  class="bg-gradient-to-r from-fuchsia-500 to-pink-500 h-full rounded-full transition-all duration-500"
                  :style="{ width: `${hostDiskUsedPercent}%` }"
                />
              </div>
              <div class="flex justify-between text-[10px] text-zinc-500 font-mono">
                <span>{{ hostDiskUsedPercent }}% used</span>
                <span>local-lvm ({{ hostDiskTotalGB }} GB)</span>
              </div>
            </div>

            <!-- CPU Card -->
            <div class="p-3 rounded-lg bg-zinc-950/70 border border-zinc-800/60 space-y-2">
              <div class="flex items-center justify-between text-[11px]">
                <span class="text-zinc-400 flex items-center gap-1.5">
                  <Cpu class="w-3.5 h-3.5 text-emerald-400" />
                  {{ t('proxmox_cpu_load') }}
                </span>
                <span class="font-mono font-bold text-emerald-300">
                  {{ hostCpuUsage }}%
                </span>
              </div>
              <div class="w-full bg-zinc-800 h-1.5 rounded-full overflow-hidden">
                <div
                  class="bg-gradient-to-r from-emerald-500 to-teal-400 h-full rounded-full transition-all duration-500"
                  :style="{ width: `${Math.min(hostCpuUsage, 100)}%` }"
                />
              </div>
              <div class="flex justify-between text-[10px] text-zinc-500 font-mono">
                <span>{{ hostCpuCores }} Cores</span>
                <span>pve host</span>
              </div>
            </div>
          </div>
        </div>

        <!-- 2. Creating State Progress Banner (shown when isCreating is true) -->
        <div
          v-if="isCreating"
          class="rounded-md bg-cyan-950/30 border border-cyan-500/40 p-5 space-y-4 animate-fade-in"
        >
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-2.5">
              <RotateCw class="w-5 h-5 text-cyan-400 animate-spin" />
              <div>
                <h4 class="text-sm font-bold text-cyan-200">
                  {{ t('add_worker_creating') }}
                </h4>
                <p class="text-xs text-zinc-400 mt-0.5">
                  VMID {{ vmid }} • {{ name }}
                </p>
              </div>
            </div>
            <div class="flex items-center gap-1.5 font-mono text-xs text-cyan-300 px-2.5 py-1 rounded-md bg-zinc-900 border border-cyan-800/50">
              <Clock class="w-3.5 h-3.5 text-cyan-400" />
              <span>{{ formatElapsed(elapsedSeconds) }}</span>
            </div>
          </div>

          <!-- Provisioning Steps Visualizer -->
          <div class="space-y-2 pt-1">
            <div
              :class="[
                'flex items-center gap-3 p-2.5 rounded-lg border text-xs transition-all',
                currentStep >= 1 ? 'bg-zinc-900/80 border-cyan-800/50 text-zinc-200' : 'bg-zinc-950/40 border-zinc-800/40 text-zinc-500',
              ]"
            >
              <div :class="['w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-bold shrink-0', currentStep > 1 ? 'bg-emerald-500/20 text-emerald-400' : currentStep === 1 ? 'bg-cyan-500/20 text-cyan-300' : 'bg-zinc-800 text-zinc-500']">
                <CheckCircle2 v-if="currentStep > 1" class="w-3.5 h-3.5 text-emerald-400" />
                <span v-else>1</span>
              </div>
              <span class="flex-1">{{ t('add_worker_step_alloc') }}</span>
              <span v-if="currentStep === 1" class="text-[11px] font-mono text-cyan-400 ">Running...</span>
            </div>

            <div
              :class="[
                'flex items-center gap-3 p-2.5 rounded-lg border text-xs transition-all',
                currentStep >= 2 ? 'bg-zinc-900/80 border-cyan-800/50 text-zinc-200' : 'bg-zinc-950/40 border-zinc-800/40 text-zinc-500',
              ]"
            >
              <div :class="['w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-bold shrink-0', currentStep > 2 ? 'bg-emerald-500/20 text-emerald-400' : currentStep === 2 ? 'bg-cyan-500/20 text-cyan-300' : 'bg-zinc-800 text-zinc-500']">
                <CheckCircle2 v-if="currentStep > 2" class="w-3.5 h-3.5 text-emerald-400" />
                <span v-else>2</span>
              </div>
              <span class="flex-1">{{ t('add_worker_step_create') }}</span>
              <span v-if="currentStep === 2" class="text-[11px] font-mono text-cyan-400 ">Running...</span>
            </div>

            <div
              :class="[
                'flex items-center gap-3 p-2.5 rounded-lg border text-xs transition-all',
                currentStep >= 3 ? 'bg-zinc-900/80 border-cyan-800/50 text-zinc-200' : 'bg-zinc-950/40 border-zinc-800/40 text-zinc-500',
              ]"
            >
              <div :class="['w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-bold shrink-0', currentStep > 3 ? 'bg-emerald-500/20 text-emerald-400' : currentStep === 3 ? 'bg-cyan-500/20 text-cyan-300' : 'bg-zinc-800 text-zinc-500']">
                <CheckCircle2 v-if="currentStep > 3" class="w-3.5 h-3.5 text-emerald-400" />
                <span v-else>3</span>
              </div>
              <span class="flex-1">{{ t('add_worker_step_iso') }}</span>
              <span v-if="currentStep === 3" class="text-[11px] font-mono text-cyan-400 ">Running...</span>
            </div>

            <div
              :class="[
                'flex items-center gap-3 p-2.5 rounded-lg border text-xs transition-all',
                currentStep >= 4 ? 'bg-zinc-900/80 border-cyan-800/50 text-zinc-200' : 'bg-zinc-950/40 border-zinc-800/40 text-zinc-500',
              ]"
            >
              <div :class="['w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-bold shrink-0', currentStep === 4 ? 'bg-cyan-500/20 text-cyan-300' : 'bg-zinc-800 text-zinc-500']">
                <span>4</span>
              </div>
              <span class="flex-1">{{ t('add_worker_step_start') }}</span>
              <span v-if="currentStep === 4" class="text-[11px] font-mono text-cyan-400 ">Running...</span>
            </div>
          </div>
        </div>

        <!-- 3. Form Inputs -->
        <div v-else class="space-y-5">
          <!-- Row 1: Node Name & VMID -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <!-- Node Name -->
            <div class="space-y-1.5">
              <label for="worker-name" class="block text-xs font-semibold text-zinc-300">
                {{ t('add_worker_name') }}
              </label>
              <div class="relative">
                <Server class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-zinc-500" />
                <input
                  id="worker-name"
                  v-model="name"
                  type="text"
                  :placeholder="t('add_worker_name_placeholder')"
                  :class="[
                    'w-full pl-9 pr-3 py-2 rounded-md bg-zinc-900 border text-xs font-mono text-zinc-100 placeholder-zinc-500 focus:outline-none focus:ring-1 transition-all',
                    !isNameValid
                      ? 'border-rose-500/70 focus:border-rose-500 focus:ring-rose-500/30'
                      : 'border-zinc-800 focus:border-cyan-500/70 focus:ring-cyan-500/30'
                  ]"
                />
              </div>
              <p v-if="!isNameValid" class="text-[11px] text-rose-400 flex items-center gap-1 mt-1">
                <AlertCircle class="w-3.5 h-3.5 shrink-0" />
                <span>Имя должно соответствовать RFC 1123 (строчные буквы a-z, цифры 0-9, дефис, 1–63 символа)</span>
              </p>
            </div>

            <!-- VMID -->
            <div class="space-y-1.5">
              <div class="flex items-center justify-between">
                <label for="worker-vmid" class="block text-xs font-semibold text-zinc-300">
                  {{ t('add_worker_vmid') }}
                </label>
                <span class="text-[10px] text-zinc-500 font-mono">
                  {{ t('add_worker_vmid_hint') }}
                </span>
              </div>
              <div class="flex items-center gap-2">
                <input
                  id="worker-vmid"
                  v-model.number="vmid"
                  type="number"
                  min="100"
                  max="9999"
                  class="flex-1 px-3 py-2 rounded-md bg-zinc-900 border border-zinc-800 text-xs font-mono text-cyan-300 focus:outline-none focus:border-cyan-500/70 focus:ring-1 focus:ring-cyan-500/30 transition-all"
                />
                <button
                  type="button"
                  @click="refreshVMID"
                  :disabled="loadingVMID"
                  class="p-2 rounded-md bg-zinc-900 hover:bg-zinc-800 border border-zinc-800 text-zinc-400 hover:text-cyan-300 transition-colors cursor-pointer disabled:opacity-40"
                  :title="t('add_worker_refresh_vmid')"
                >
                  <RotateCw :class="['w-4 h-4', loadingVMID ? 'animate-spin' : '']" />
                </button>
              </div>
            </div>
          </div>

          <!-- Row 2: Node Role (Worker Only) -->
          <div class="space-y-1.5">
            <label class="block text-xs font-semibold text-zinc-300">
              {{ t('add_worker_role') }}
            </label>
            <div class="p-3 rounded-md bg-zinc-900/90 border border-cyan-500/40 flex items-start gap-3">
              <div class="w-8 h-8 rounded-lg bg-sky-500/10 border border-sky-500/30 flex items-center justify-center text-sky-400 shrink-0 mt-0.5">
                <Cpu class="w-4 h-4" />
              </div>
              <div class="flex-1">
                <div class="flex items-center justify-between">
                  <span class="text-xs font-bold text-sky-300">
                    {{ t('add_worker_role_worker') }}
                  </span>
                  <span class="text-[10px] font-mono px-2 py-0.5 rounded bg-sky-950/80 text-sky-300 border border-sky-800/40">
                    node.role={{ role }}
                  </span>
                </div>
                <p class="text-[11px] text-zinc-400 mt-1 leading-relaxed">
                  {{ t('add_worker_role_worker_desc') }}
                </p>
              </div>
            </div>
          </div>

          <!-- Row 3: CPU Cores (1 - 4) -->
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <label for="worker-cores" class="block text-xs font-semibold text-zinc-300 flex items-center gap-1.5">
                <Cpu class="w-3.5 h-3.5 text-cyan-400" />
                {{ t('add_worker_cores') }}
              </label>
              <span class="font-mono text-xs font-bold text-cyan-300">
                {{ cores }} {{ t('add_worker_cores_unit') }}
              </span>
            </div>

            <!-- Preset Pills -->
            <div class="grid grid-cols-4 gap-2">
              <button
                v-for="c in cpuPresets"
                :key="c"
                type="button"
                @click="cores = c"
                :class="[
                  'py-1.5 px-3 rounded-md text-xs font-mono font-medium border transition-all cursor-pointer text-center',
                  cores === c
                    ? 'bg-cyan-500/20 border-cyan-500/50 text-cyan-200 '
                    : 'bg-zinc-900/80 border-zinc-800 text-zinc-400 hover:text-zinc-200 hover:border-zinc-700',
                ]"
              >
                {{ c }} Core{{ c > 1 ? 's' : '' }}
              </button>
            </div>

            <input
              id="worker-cores"
              v-model.number="cores"
              type="range"
              min="1"
              max="4"
              step="1"
              class="w-full accent-cyan-400 cursor-pointer"
            />
          </div>

          <!-- Row 4: RAM (2 - 8 GB) -->
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <label for="worker-ram" class="block text-xs font-semibold text-zinc-300 flex items-center gap-1.5">
                <Layers class="w-3.5 h-3.5 text-indigo-400" />
                {{ t('add_worker_ram') }}
              </label>
              <div class="flex items-center gap-2 font-mono text-xs">
                <span class="font-bold text-indigo-300">{{ memoryGB }} {{ t('add_worker_ram_unit') }}</span>
                <span class="text-zinc-500">({{ memoryGB * 1024 }} MB)</span>
              </div>
            </div>

            <!-- RAM Presets -->
            <div class="grid grid-cols-5 gap-2">
              <button
                v-for="r in ramPresets"
                :key="r"
                type="button"
                @click="memoryGB = r"
                :class="[
                  'py-1.5 px-2 rounded-md text-xs font-mono font-medium border transition-all cursor-pointer text-center',
                  memoryGB === r
                    ? 'bg-indigo-500/20 border-indigo-500/50 text-indigo-200 '
                    : 'bg-zinc-900/80 border-zinc-800 text-zinc-400 hover:text-zinc-200 hover:border-zinc-700',
                ]"
              >
                {{ r }} GB
              </button>
            </div>

            <input
              id="worker-ram"
              v-model.number="memoryGB"
              type="range"
              min="2"
              max="8"
              step="1"
              class="w-full accent-indigo-400 cursor-pointer"
            />
          </div>

          <!-- Row 5: Disk Size (20 - 100 GB) -->
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <label for="worker-disk" class="block text-xs font-semibold text-zinc-300 flex items-center gap-1.5">
                <HardDrive class="w-3.5 h-3.5 text-fuchsia-400" />
                {{ t('add_worker_disk') }}
              </label>
              <div class="flex items-center gap-2 font-mono text-xs">
                <span class="font-bold text-fuchsia-300">{{ diskGB }} {{ t('add_worker_disk_unit') }}</span>
                <span class="text-zinc-500 font-sans">({{ storage }})</span>
              </div>
            </div>

            <!-- Disk Presets -->
            <div class="grid grid-cols-5 gap-2">
              <button
                v-for="d in diskPresets"
                :key="d"
                type="button"
                @click="diskGB = d"
                :class="[
                  'py-1.5 px-2 rounded-md text-xs font-mono font-medium border transition-all cursor-pointer text-center',
                  diskGB === d
                    ? 'bg-fuchsia-500/20 border-fuchsia-500/50 text-fuchsia-200 '
                    : 'bg-zinc-900/80 border-zinc-800 text-zinc-400 hover:text-zinc-200 hover:border-zinc-700',
                ]"
              >
                {{ d }} GB
              </button>
            </div>

            <input
              id="worker-disk"
              v-model.number="diskGB"
              type="range"
              min="20"
              max="100"
              step="10"
              class="w-full accent-fuchsia-400 cursor-pointer"
            />
          </div>

          <!-- Row 6: Network & AutoStart Options -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-1">
            <!-- Storage & Bridge info -->
            <div class="p-3 rounded-md bg-zinc-900/60 border border-zinc-800/80 space-y-1.5 text-xs text-zinc-400">
              <div class="flex items-center justify-between">
                <span>{{ t('add_worker_storage') }}:</span>
                <span class="font-mono text-zinc-200 font-semibold">{{ storage }}</span>
              </div>
              <div class="flex items-center justify-between">
                <span>{{ t('add_worker_bridge') }}:</span>
                <span class="font-mono text-zinc-200 font-semibold">{{ bridge }}</span>
              </div>
            </div>

            <!-- Auto-start checkbox -->
            <label class="p-3 rounded-md bg-zinc-900/60 border border-zinc-800/80 flex items-center gap-3 cursor-pointer hover:border-zinc-700 transition-colors">
              <input
                v-model="autoStart"
                type="checkbox"
                class="w-4 h-4 rounded bg-zinc-800 border-zinc-700 text-cyan-500 focus:ring-cyan-500/40 accent-cyan-500 cursor-pointer"
              />
              <span class="text-xs text-zinc-300 font-medium">
                {{ t('add_worker_autostart') }}
              </span>
            </label>
          </div>
        </div>
      </div>

      <!-- Modal Footer -->
      <div class="px-6 py-4 bg-zinc-900/60 border-t border-zinc-800/80 flex items-center justify-between gap-3 shrink-0">
        <div class="flex items-center gap-1.5 text-xs text-zinc-500 font-medium">
          <Sparkles class="w-3.5 h-3.5 text-cyan-400" />
          <span>Talos Linux ISO + QEMU Guest Agent</span>
        </div>

        <div class="flex items-center gap-3">
          <button
            type="button"
            @click="emit('close')"
            :disabled="isCreating"
            class="px-4 py-2 rounded-md bg-zinc-800 hover:bg-zinc-700 text-xs font-semibold text-zinc-300 transition-colors cursor-pointer disabled:opacity-50"
          >
            {{ t('close') }}
          </button>

          <button
            type="button"
            @click="handleSubmit"
            :disabled="isCreating || !isNameValid"
            class="flex items-center gap-2 px-5 py-2 rounded-md bg-gradient-to-r from-cyan-600 to-emerald-600 hover:from-cyan-500 hover:to-emerald-500 text-white text-xs font-semibold  shadow-cyan-900/40 transition-all cursor-pointer active:scale-95 disabled:opacity-50"
          >
            <RotateCw v-if="isCreating" class="w-4 h-4 animate-spin" />
            <Zap v-else class="w-4 h-4" />
            <span>{{ isCreating ? t('add_worker_creating') : t('add_worker_submit_btn') }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.custom-scrollbar::-webkit-scrollbar {
  width: 5px;
}
.custom-scrollbar::-webkit-scrollbar-track {
  background: rgba(24, 24, 27, 0.4);
}
.custom-scrollbar::-webkit-scrollbar-thumb {
  background: rgba(63, 63, 70, 0.6);
  border-radius: 3px;
}
</style>

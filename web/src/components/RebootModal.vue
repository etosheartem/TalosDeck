<script setup lang="ts">
import { ref } from 'vue'
import { AlertTriangle, RotateCw, X } from 'lucide-vue-next'
import { t } from '../i18n'
import { rebootNode } from '../api'
import type { NodeOverview } from '../types'

const props = defineProps<{
  node: NodeOverview | null
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'success', message: string): void
  (e: 'error', message: string): void
}>()

const loading = ref(false)

const handleConfirm = async () => {
  if (!props.node) return
  loading.value = true
  try {
    const res = await rebootNode(props.node.ip)
    if (res.success) {
      emit('success', `${t('reboot_success')}: ${props.node.hostname} (${props.node.ip})`)
      emit('close')
    } else {
      emit('error', res.message || t('reboot_error'))
    }
  } catch (err: any) {
    emit('error', err.message || t('reboot_error'))
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div
    v-if="open && node"
    class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/80 backdrop-blur-sm animate-fade-in"
    @click.self="emit('close')"
  >
    <div
      class="w-full max-w-md rounded-2xl bg-zinc-950 border border-rose-900/50 shadow-2xl shadow-rose-950/30 overflow-hidden"
    >
      <!-- Header with Danger Icon -->
      <div class="px-6 pt-6 pb-4 flex items-start gap-4">
        <div class="p-3 rounded-xl bg-rose-950/70 border border-rose-800/80 text-rose-400 shrink-0">
          <AlertTriangle class="w-6 h-6" />
        </div>
        <div class="flex-1">
          <h2 class="text-base font-bold text-zinc-100">
            {{ t('reboot_title') }}
          </h2>
          <p class="text-xs text-zinc-400 mt-1">
            {{ t('reboot_warning') }}
            <span class="font-mono font-semibold text-rose-300">{{ node.hostname }}</span>
            (<span class="font-mono text-zinc-300">{{ node.ip }}</span>)?
          </p>
        </div>
        <button
          @click="emit('close')"
          class="text-zinc-500 hover:text-zinc-300 transition-colors p-1"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Warning Description Box -->
      <div class="px-6 py-3">
        <div class="p-3 rounded-xl bg-rose-950/30 border border-rose-900/40 text-xs text-rose-300/90 leading-relaxed">
          {{ t('reboot_danger_text') }}
        </div>
      </div>

      <!-- Action Buttons -->
      <div class="px-6 py-4 bg-zinc-900/50 border-t border-zinc-800/80 flex items-center justify-end gap-3">
        <button
          @click="emit('close')"
          :disabled="loading"
          class="px-4 py-2 rounded-lg bg-zinc-800 hover:bg-zinc-700 text-xs font-semibold text-zinc-300 transition-colors cursor-pointer disabled:opacity-50"
        >
          {{ t('reboot_cancel_btn') }}
        </button>
        <button
          @click="handleConfirm"
          :disabled="loading"
          class="flex items-center gap-2 px-4 py-2 rounded-lg bg-rose-600 hover:bg-rose-500 text-white text-xs font-semibold shadow-lg shadow-rose-900/40 transition-all cursor-pointer active:scale-95 disabled:opacity-50"
        >
          <RotateCw :class="['w-3.5 h-3.5', loading ? 'animate-spin' : '']" />
          <span>{{ loading ? t('reboot_in_progress') : t('reboot_confirm_btn') }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

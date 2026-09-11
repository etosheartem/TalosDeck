<script setup lang="ts">
import { CheckCircle2, AlertCircle, X } from 'lucide-vue-next'
import { t } from '../i18n'

defineProps<{
  show: boolean
  message: string
  type: 'success' | 'error' | 'info'
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()
</script>

<template>
  <Transition
    enter-active-class="transform transition ease-out duration-300"
    enter-from-class="translate-y-2 opacity-0 sm:translate-y-0 sm:translate-x-2"
    enter-to-class="translate-y-0 opacity-100 sm:translate-x-0"
    leave-active-class="transition ease-in duration-100"
    leave-from-class="opacity-100"
    leave-to-class="opacity-0"
  >
    <div
      v-if="show"
      class="fixed bottom-5 left-5 right-5 sm:left-auto z-50 max-w-sm rounded-xl p-3.5 shadow-2xl backdrop-blur-md flex items-center gap-3 border text-xs font-medium"
      :class="[
        type === 'success'
          ? 'bg-emerald-950/90 text-emerald-200 border-emerald-800/80 shadow-emerald-950/50'
          : type === 'error'
          ? 'bg-rose-950/90 text-rose-200 border-rose-800/80 shadow-rose-950/50'
          : 'bg-zinc-900/90 text-zinc-200 border-zinc-800 shadow-black',
      ]"
    >
      <CheckCircle2 v-if="type === 'success'" class="w-4 h-4 text-emerald-400 shrink-0" />
      <AlertCircle v-else-if="type === 'error'" class="w-4 h-4 text-rose-400 shrink-0" />

      <span class="flex-1">{{ message }}</span>

      <button
        @click="emit('close')"
        class="text-zinc-400 hover:text-white p-1 rounded transition-colors cursor-pointer"
        :title="t('close')"
        :aria-label="t('close')"
      >
        <X class="w-3.5 h-3.5" />
      </button>
    </div>
  </Transition>
</template>

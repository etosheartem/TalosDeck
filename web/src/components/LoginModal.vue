<script setup lang="ts">
import { ref } from 'vue'
import {
  Lock,
  KeyRound,
  ShieldCheck,
  AlertCircle,
  X,
  Eye,
  EyeOff,
  Check,
  Loader2,
} from 'lucide-vue-next'
import { t } from '../i18n'
import { login } from '../api'
import type { UserInfo } from '../types'

defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'success', user: UserInfo): void
}>()

const password = ref('')
const showPassword = ref(false)
const loading = ref(false)
const errorMessage = ref('')
const successMessage = ref('')

const handleLogin = async () => {
  if (!password.value.trim()) {
    errorMessage.value = t('auth_password_required')
    return
  }

  loading.value = true
  errorMessage.value = ''
  successMessage.value = ''

  try {
    const res = await login(password.value)
    if (res.success && res.user) {
      successMessage.value = t('auth_success')
      setTimeout(() => {
        password.value = ''
        emit('success', res.user!)
        emit('close')
      }, 400)
    } else {
      errorMessage.value = res.error || t('auth_error_invalid')
    }
  } catch (err: any) {
    errorMessage.value = err.message || t('auth_error_invalid')
  } finally {
    loading.value = false
  }
}

const fillDefaultPassword = () => {
  password.value = 'admin'
  errorMessage.value = ''
}
</script>

<template>
  <div
    v-if="open"
    class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/80  animate-fade-in"
    @click.self="emit('close')"
  >
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="login-modal-title"
      class="w-full max-w-md bg-zinc-900 border border-zinc-800/90 rounded-lg p-6  space-y-5 relative"
    >
      <!-- Close Button -->
      <button
        @click="emit('close')"
        class="absolute top-4 right-4 p-1.5 rounded-lg text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800 transition-colors cursor-pointer"
        :title="t('close')"
        :aria-label="t('close')"
      >
        <X class="w-4 h-4" />
      </button>

      <!-- Modal Header -->
      <div class="flex items-center gap-3.5">
        <div class="p-3 rounded-md bg-emerald-950/60 text-emerald-400 border border-emerald-800/60 shadow-[0_0_12px_rgba(16,185,129,0.25)]">
          <ShieldCheck class="w-6 h-6" />
        </div>
        <div>
          <h3 id="login-modal-title" class="text-sm font-semibold text-zinc-100">{{ t('auth_modal_title') }}</h3>
          <p class="text-xs text-zinc-400 mt-0.5">{{ t('auth_modal_desc') }}</p>
        </div>
      </div>

      <!-- Error Notification -->
      <div
        v-if="errorMessage"
        class="p-3 rounded-md bg-red-950/50 border border-red-800/60 text-xs text-red-300 flex items-center gap-2.5"
      >
        <AlertCircle class="w-4 h-4 text-red-400 shrink-0" />
        <span>{{ errorMessage }}</span>
      </div>

      <!-- Success Notification -->
      <div
        v-if="successMessage"
        class="p-3 rounded-md bg-emerald-950/50 border border-emerald-800/60 text-xs text-emerald-300 flex items-center gap-2.5"
      >
        <Check class="w-4 h-4 text-emerald-400 shrink-0" />
        <span>{{ successMessage }}</span>
      </div>

      <!-- Form -->
      <form @submit.prevent="handleLogin" class="space-y-4">
        <div class="space-y-1.5">
          <label for="login-password" class="text-xs font-semibold text-zinc-300 flex items-center justify-between">
            <span class="flex items-center gap-1.5">
              <KeyRound class="w-3.5 h-3.5 text-zinc-400" />
              <span>{{ t('auth_password_label') }}</span>
            </span>
            <button
              type="button"
              @click="fillDefaultPassword"
              class="text-[11px] text-orange-400 hover:text-orange-300 hover:underline cursor-pointer"
            >
              {{ t('auth_use_default') }} ("admin")
            </button>
          </label>
          <div class="relative">
            <input
              id="login-password"
              :type="showPassword ? 'text' : 'password'"
              v-model="password"
              :placeholder="t('auth_password_placeholder')"
              class="w-full bg-zinc-950 border border-zinc-800 rounded-md px-3.5 py-2.5 text-sm text-zinc-100 placeholder:text-zinc-600 focus:outline-none focus:border-orange-500/80 transition-colors pr-10"
              autocomplete="current-password"
              autofocus
            />
            <button
              type="button"
              @click="showPassword = !showPassword"
              class="absolute right-3 top-1/2 -translate-y-1/2 text-zinc-500 hover:text-zinc-300 cursor-pointer p-1"
            >
              <EyeOff v-if="showPassword" class="w-4 h-4" />
              <Eye v-else class="w-4 h-4" />
            </button>
          </div>
          <p class="text-[11px] text-zinc-500 pt-0.5">
            {{ t('auth_quick_hint') }}
          </p>
        </div>

        <!-- Submit & Cancel Buttons -->
        <div class="flex items-center justify-end gap-3 pt-2">
          <button
            type="button"
            @click="emit('close')"
            :disabled="loading"
            class="px-4 py-2 rounded-md bg-zinc-800 hover:bg-zinc-700 text-xs font-semibold text-zinc-300 transition-all cursor-pointer disabled:opacity-50"
          >
            {{ t('reboot_cancel_btn') }}
          </button>
          <button
            type="submit"
            :disabled="loading"
            class="flex items-center gap-2 px-5 py-2 rounded-md bg-emerald-600 hover:bg-emerald-500 text-xs font-semibold text-white transition-all cursor-pointer active:scale-95 disabled:opacity-50  shadow-emerald-950/50"
          >
            <Loader2 v-if="loading" class="w-3.5 h-3.5 animate-spin" />
            <Lock v-else class="w-3.5 h-3.5" />
            <span>{{ loading ? t('auth_logging_in') : t('auth_login_btn') }}</span>
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

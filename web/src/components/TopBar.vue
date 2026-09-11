<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Menu, RefreshCw, LogIn, LogOut, ShieldCheck } from 'lucide-vue-next'
import { t } from '../i18n'
import type { TabKey, ClusterInfo, UserInfo } from '../types'
import { isAuthenticated, logout, getMe } from '../api'
import LoginModal from './LoginModal.vue'

defineProps<{
  activeTab: TabKey
  cluster: ClusterInfo
  loading: boolean
  autoRefreshInterval: number
  lastUpdated: Date | null
}>()

const emit = defineEmits<{
  (e: 'toggleMobile'): void
  (e: 'refresh'): void
  (e: 'update:autoRefreshInterval', val: number): void
  (e: 'show-toast', payload: { message: string; type: 'success' | 'error' | 'info' }): void
}>()

const isLoginModalOpen = ref(false)
onMounted(() => getMe())

const autoRefreshOptions = [
  { value: 0, labelKey: 'auto_refresh_off' },
  { value: 5000, labelKey: 'auto_refresh_5s' },
  { value: 10000, labelKey: 'auto_refresh_10s' },
  { value: 30000, labelKey: 'auto_refresh_30s' },
]

const tabTitles: Record<TabKey, string> = {
  nodes: 'tab_nodes',
  storage: 'tab_storage',
  config: 'tab_config',
  workloads: 'tab_workloads',
  operations: 'tab_operations',
}

const onAutoRefreshChange = (event: Event) => {
  emit('update:autoRefreshInterval', Number((event.target as HTMLSelectElement).value))
}

const handleLogout = async () => {
  await logout()
  emit('show-toast', { message: t('auth_logged_out'), type: 'info' })
}

const onLoginSuccess = (user: UserInfo) => {
  emit('show-toast', { message: `${t('auth_success')} (${user.username})`, type: 'success' })
}
</script>

<template>
  <header class="sticky top-0 z-20 flex h-14 items-center justify-between gap-4 border-b px-4 sm:px-6" style="border-color: var(--border); background: color-mix(in srgb, var(--canvas) 94%, transparent)">
    <div class="flex min-w-0 items-center gap-3">
      <button class="control flex h-8 w-8 shrink-0 items-center justify-center lg:hidden" @click="emit('toggleMobile')">
        <Menu class="h-4 w-4" />
      </button>
      <div class="min-w-0">
        <h1 class="truncate text-sm font-semibold">{{ t(tabTitles[activeTab]) }}</h1>
        <p class="mono mt-0.5 hidden truncate text-[10px] sm:block" style="color: var(--text-faint)">
          {{ cluster.endpoint }}<span v-if="lastUpdated"> · {{ lastUpdated.toLocaleTimeString() }}</span>
        </p>
      </div>
    </div>

    <div class="flex shrink-0 items-center gap-2">
      <label class="control hidden items-center gap-2 px-2 sm:flex">
        <span class="text-[10px] uppercase tracking-wide" style="color: var(--text-faint)">{{ t('auto_refresh') }}</span>
        <select :value="autoRefreshInterval" class="bg-transparent text-xs outline-none" @change="onAutoRefreshChange">
          <option v-for="option in autoRefreshOptions" :key="option.value" :value="option.value" style="background: var(--surface)">
            {{ t(option.labelKey) }}
          </option>
        </select>
      </label>

      <div v-if="isAuthenticated" class="hidden items-center gap-1 sm:flex">
        <span class="status-chip is-ok"><ShieldCheck class="h-3 w-3" /> Admin</span>
        <button class="control flex h-8 items-center gap-1.5 px-2" :title="t('auth_logout')" @click="handleLogout">
          <LogOut class="h-3.5 w-3.5" />
          <span class="text-xs">{{ t('auth_logout') }}</span>
        </button>
      </div>
      <button v-else class="control flex h-8 items-center gap-1.5 px-2.5" @click="isLoginModalOpen = true">
        <LogIn class="h-3.5 w-3.5" />
        <span class="hidden text-xs sm:inline">{{ t('auth_status_viewer') }}</span>
      </button>

      <button class="control flex h-8 items-center gap-1.5 px-2.5" :disabled="loading" @click="emit('refresh')">
        <RefreshCw :class="['h-3.5 w-3.5', { 'animate-spin': loading }]" />
        <span class="hidden text-xs sm:inline">{{ t('refresh') }}</span>
      </button>
    </div>

    <LoginModal :open="isLoginModalOpen" @close="isLoginModalOpen = false" @success="onLoginSuccess" />
  </header>
</template>

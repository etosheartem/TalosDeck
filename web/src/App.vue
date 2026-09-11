<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, nextTick } from 'vue'
import {
  Info,
  ExternalLink,
} from 'lucide-vue-next'
import { t, currentLocale } from './i18n'
import { fetchNodes, fetchClusterInfo, fetchEtcdStatus, fetchPods } from './api'
import type { ClusterInfo, NodeOverview, TabKey } from './types'

import Sidebar from './components/Sidebar.vue'
import TopBar from './components/TopBar.vue'
import ClusterStats from './components/ClusterStats.vue'
import NodesView from './components/views/NodesView.vue'
import StorageView from './components/views/StorageView.vue'
import MachineConfigView from './components/views/MachineConfigView.vue'
import WorkloadsView from './components/views/WorkloadsView.vue'
import OperationsView from './components/views/OperationsView.vue'

import ServicesModal from './components/ServicesModal.vue'
import LogsModal from './components/LogsModal.vue'
import RebootModal from './components/RebootModal.vue'
import Toast from './components/Toast.vue'

// Active tab state
const activeTab = ref<TabKey>('nodes')
const mobileOpen = ref(false)

// Reactive state
const loading = ref(false)
const isMock = ref(false)
const loadError = ref('')
const lastUpdated = ref<Date | null>(null)
const nodes = ref<NodeOverview[]>([])
const podCount = ref(0)
const etcdHealthy = ref(true)

const cluster = ref<ClusterInfo>({
  name: 'lab-k8s',
  healthy: true,
  talosVersion: 'v1.14.0',
  kubernetesVersion: 'v1.32.2',
  endpoint: 'https://10.42.0.110:6443',
  totalNodes: 0,
  readyNodes: 0,
  controlPlaneCount: 0,
  workerCount: 0,
})

// Auto refresh interval in ms (0 = off)
const autoRefreshInterval = ref<number>(10000)
let autoRefreshTimer: ReturnType<typeof setTimeout> | null = null
let isUnmounted = false

// Modals
const activeServicesNode = ref<NodeOverview | null>(null)
const isServicesOpen = ref(false)

const activeLogsNode = ref<NodeOverview | null>(null)
const isLogsOpen = ref(false)

const activeRebootNode = ref<NodeOverview | null>(null)
const isRebootOpen = ref(false)

// UI-01 & UI-02: Modal management (body scroll lock & Escape listener)
let modalObserver: MutationObserver | null = null

const updateScrollLock = () => {
  if (typeof document === 'undefined') return
  const isAppModalOpen = isServicesOpen.value || isLogsOpen.value || isRebootOpen.value
  const isDomModalOpen = Boolean(document.querySelector('[role="dialog"]'))
  if (isAppModalOpen || isDomModalOpen) {
    document.body.style.overflow = 'hidden'
  } else {
    document.body.style.overflow = ''
  }
}

watch([isServicesOpen, isLogsOpen, isRebootOpen], (states) => {
  if (states.some(Boolean)) {
    if (typeof document !== 'undefined') {
      document.body.style.overflow = 'hidden'
    }
  } else {
    nextTick(() => {
      updateScrollLock()
    })
  }
})

const handleKeyDown = (e: KeyboardEvent) => {
  if (e.key === 'Escape') {
    if (isServicesOpen.value) {
      isServicesOpen.value = false
      return
    }
    if (isLogsOpen.value) {
      isLogsOpen.value = false
      return
    }
    if (isRebootOpen.value) {
      isRebootOpen.value = false
      return
    }
    if (typeof document !== 'undefined') {
      const dialog = document.querySelector('[role="dialog"]')
      if (dialog) {
        const xIcon = dialog.querySelector('svg.lucide-x') || dialog.parentElement?.querySelector('svg.lucide-x')
        const closeBtn = xIcon?.closest('button') || dialog.querySelector<HTMLButtonElement>('button[aria-label="Close"], button[aria-label="close"]')
        if (closeBtn) {
          closeBtn.click()
          return
        }
      }
    }
    if (mobileOpen.value) {
      mobileOpen.value = false
    }
  }
}

// Toast
const toast = ref<{
  show: boolean
  message: string
  type: 'success' | 'error' | 'info'
}>({
  show: false,
  message: '',
  type: 'info',
})
let toastTimer: ReturnType<typeof setTimeout> | null = null

const showToast = (message: string, type: 'success' | 'error' | 'info' = 'info') => {
  if (toastTimer) clearTimeout(toastTimer)
  toast.value = { show: true, message, type }
  toastTimer = setTimeout(() => {
    toast.value.show = false
  }, 4000)
}

// Fetch data
const loadData = async () => {
  if (loading.value) return

  loading.value = true
  loadError.value = ''
  try {
    const res = await fetchNodes()
    nodes.value = res.nodes
    isMock.value = res.isMock
    cluster.value = await fetchClusterInfo(nodes.value)

    // Check etcd health
    const etcdRes = await fetchEtcdStatus()
    etcdHealthy.value = etcdRes.healthy

    // Check pods count
    const podsRes = await fetchPods()
    podCount.value = podsRes.pods?.length || 0
    lastUpdated.value = new Date()
  } catch (err) {
    console.error('Failed to load cluster data:', err)
    loadError.value = err instanceof Error ? err.message : 'Failed to load cluster data'
  } finally {
    loading.value = false
  }
}

// Auto refresh setup
const setupAutoRefresh = () => {
  if (autoRefreshTimer) {
    clearTimeout(autoRefreshTimer)
    autoRefreshTimer = null
  }
  if (!isUnmounted && autoRefreshInterval.value > 0) {
    autoRefreshTimer = setTimeout(async () => {
      autoRefreshTimer = null
      await loadData()
      setupAutoRefresh()
    }, autoRefreshInterval.value)
  }
}

watch(autoRefreshInterval, () => {
  setupAutoRefresh()
})

onMounted(async () => {
  isUnmounted = false
  await loadData()
  setupAutoRefresh()

  // UI-01: Watch DOM for child component modals
  if (typeof MutationObserver !== 'undefined') {
    modalObserver = new MutationObserver(() => {
      updateScrollLock()
    })
    modalObserver.observe(document.body, { childList: true, subtree: true })
  }
  updateScrollLock()

  // UI-02: Add global Escape keydown listener
  window.addEventListener('keydown', handleKeyDown)
})

onUnmounted(() => {
  isUnmounted = true
  if (autoRefreshTimer) clearTimeout(autoRefreshTimer)
  if (toastTimer) clearTimeout(toastTimer)

  // UI-01: Clean up body scroll lock and observer
  if (modalObserver) {
    modalObserver.disconnect()
    modalObserver = null
  }
  if (typeof document !== 'undefined') {
    document.body.style.overflow = ''
  }

  // UI-02: Clean up Escape key listener
  window.removeEventListener('keydown', handleKeyDown)
})

// Modal Open Handlers
const onOpenServices = (node: NodeOverview) => {
  activeServicesNode.value = node
  isServicesOpen.value = true
}

const onOpenLogs = (node: NodeOverview) => {
  activeLogsNode.value = node
  isLogsOpen.value = true
}

const onOpenReboot = (node: NodeOverview) => {
  activeRebootNode.value = node
  isRebootOpen.value = true
}

// Reboot handlers
const onRebootSuccess = (nodeIP: string) => {
  isRebootOpen.value = false
  showToast(`${t('reboot_success')}: ${nodeIP}`, 'success')
  setTimeout(loadData, 3000)
}

const onRebootError = (errMsg: string) => {
  showToast(errMsg, 'error')
}
</script>

<template>
  <div class="min-h-screen" style="background: var(--canvas); color: var(--text)">
    <!-- Left Navigation Sidebar -->
    <Sidebar
      :activeTab="activeTab"
      :nodeCount="nodes.length"
      :podCount="podCount"
      :etcdHealthy="etcdHealthy"
      :cluster="cluster"
      :mobileOpen="mobileOpen"
      @update:activeTab="activeTab = $event"
      @closeMobile="mobileOpen = false"
    />

    <!-- Main Content Area (offset by sidebar width on desktop) -->
    <div class="flex min-h-screen flex-col lg:pl-56">
      <!-- Top Bar -->
      <TopBar
        :activeTab="activeTab"
        :cluster="cluster"
        :loading="loading"
        :autoRefreshInterval="autoRefreshInterval"
        :lastUpdated="lastUpdated"
        @toggleMobile="mobileOpen = !mobileOpen"
        @refresh="loadData"
        @update:autoRefreshInterval="autoRefreshInterval = $event"
        @show-toast="showToast($event.message, $event.type)"
      />

      <!-- Main Content Container -->
      <main class="mx-auto w-full max-w-[1680px] flex-1 space-y-4 p-4 sm:p-5 lg:p-6">
        <div
          v-if="loadError"
          class="panel flex items-start gap-2.5 px-3 py-2.5 text-[11px]"
          style="border-color: #673139; background: var(--danger-muted); color: var(--danger)"
          role="alert"
        >
          <Info class="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <div>
            <div class="font-semibold">{{ currentLocale === 'ru' ? 'Не все данные обновлены' : 'Some data could not be refreshed' }}</div>
            <div class="mono mt-1 text-[10px] opacity-80">{{ loadError }}</div>
          </div>
        </div>
        <!-- Mock Data Notice Banner if backend API is not running -->
        <div
          v-if="isMock"
          class="panel flex items-center justify-between px-3 py-2 text-[11px]"
          style="border-color: #354969; background: var(--accent-muted); color: #b9d3ff"
        >
          <div class="flex items-center gap-2.5">
            <Info class="h-3.5 w-3.5 shrink-0" />
            <span>{{ t('demo_mode_notice') }}</span>
          </div>
          <span class="mono hidden text-[10px] sm:inline" style="color: #8da6ca">
            Proxy: /api -> :8080
          </span>
        </div>

        <!-- Tab 1: Nodes View -->
        <div v-show="activeTab === 'nodes'" class="space-y-4">
          <!-- Cluster Statistics Summary -->
          <ClusterStats :cluster="cluster" />

          <!-- Nodes View Section -->
          <NodesView
            :nodes="nodes"
            @open-services="onOpenServices"
            @open-logs="onOpenLogs"
            @open-reboot="onOpenReboot"
            @refresh="loadData"
            @show-toast="showToast($event.message, $event.type)"
          />
        </div>

        <!-- Tab 2: Storage & Disks View -->
        <div v-show="activeTab === 'storage'">
          <StorageView :nodes="nodes" />
        </div>

        <!-- Tab 3: MachineConfig View -->
        <div v-show="activeTab === 'config'">
          <MachineConfigView :nodes="nodes" />
        </div>

        <!-- Tab 4: Workloads View -->
        <div v-show="activeTab === 'workloads'">
          <WorkloadsView />
        </div>

        <!-- Tab 5: Operations & etcd View -->
        <div v-show="activeTab === 'operations'">
          <OperationsView
            :nodes="nodes"
            @show-toast="showToast($event.message, $event.type)"
          />
        </div>
      </main>

      <!-- Modals -->
      <ServicesModal
        :open="isServicesOpen"
        :node="activeServicesNode"
        @close="isServicesOpen = false"
      />

      <LogsModal
        :open="isLogsOpen"
        :node="activeLogsNode"
        @close="isLogsOpen = false"
      />

      <RebootModal
        :open="isRebootOpen"
        :node="activeRebootNode"
        @close="isRebootOpen = false"
        @success="onRebootSuccess"
        @error="onRebootError"
      />

      <!-- Toast Notifications -->
      <Toast
        :show="toast.show"
        :message="toast.message"
        :type="toast.type"
        @close="toast.show = false"
      />

      <!-- Footer -->
      <footer class="mt-auto border-t px-4 py-3 text-[10px] sm:px-6" style="border-color: var(--border); color: var(--text-faint)">
        <div class="mx-auto flex max-w-[1680px] flex-col items-center justify-between gap-2 sm:flex-row">
          <div class="flex items-center gap-2">
            <span class="font-semibold" style="color: var(--text-muted)">TalosDeck v0.2</span>
            <span>•</span>
            <span>Talos Linux cluster console</span>
          </div>
          <div class="flex flex-wrap items-center gap-4">
            <a
              href="https://github.com/etosheartem/TalosDeck"
              target="_blank"
              rel="noopener noreferrer"
              class="flex items-center gap-1.5 transition-colors hover:text-white"
            >
              <svg class="h-3 w-3 fill-current" viewBox="0 0 24 24">
                <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.484 2 12.017c0 4.425 2.865 8.18 6.839 9.504.5.092.682-.217.682-.483 0-.237-.008-.868-.013-1.703-2.782.605-3.369-1.343-3.369-1.343-.454-1.158-1.11-1.466-1.11-1.466-.908-.62.069-.608.069-.608 1.003.07 1.53 1.032 1.53 1.032.892 1.53 2.341 1.088 2.91.832.092-.647.35-1.088.636-1.338-2.22-.253-4.555-1.113-4.555-4.951 0-1.093.39-1.988 1.029-2.688-.103-.253-.446-1.272.098-2.65 0 0 .84-.27 2.75 1.026A9.564 9.564 0 0112 6.844c.85.004 1.705.115 2.504.337 1.909-1.296 2.747-1.027 2.747-1.027.546 1.379.202 2.398.1 2.651.64.7 1.028 1.595 1.028 2.688 0 3.848-2.339 4.695-4.566 4.943.359.309.678.92.678 1.855 0 1.338-.012 2.419-.012 2.747 0 .268.18.58.688.482A10.019 10.019 0 0022 12.017C22 6.484 17.522 2 12 2z"/>
              </svg>
              <span class="mono">GitHub</span>
              <ExternalLink class="h-2.5 w-2.5" />
            </a>
            <a
              href="https://www.talos.dev/latest/introduction/what-is-talos/"
              target="_blank"
              rel="noopener noreferrer"
              class="flex items-center gap-1 transition-colors hover:text-white"
            >
              <span>Talos Docs</span>
              <ExternalLink class="h-2.5 w-2.5" />
            </a>
            <a
              href="https://kubernetes.io"
              target="_blank"
              rel="noopener noreferrer"
              class="flex items-center gap-1 transition-colors hover:text-white"
            >
              <span>Kubernetes</span>
              <ExternalLink class="h-2.5 w-2.5" />
            </a>
          </div>
        </div>
      </footer>
    </div>
  </div>
</template>

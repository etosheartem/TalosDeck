<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import {
  Info,
  ExternalLink,
} from 'lucide-vue-next'
import { t } from './i18n'
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
let autoRefreshTimer: ReturnType<typeof setInterval> | null = null

// Modals
const activeServicesNode = ref<NodeOverview | null>(null)
const isServicesOpen = ref(false)

const activeLogsNode = ref<NodeOverview | null>(null)
const isLogsOpen = ref(false)

const activeRebootNode = ref<NodeOverview | null>(null)
const isRebootOpen = ref(false)

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
  loading.value = true
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
  } catch (err) {
    console.error('Failed to load cluster data:', err)
  } finally {
    loading.value = false
  }
}

// Auto refresh setup
const setupAutoRefresh = () => {
  if (autoRefreshTimer) {
    clearInterval(autoRefreshTimer)
    autoRefreshTimer = null
  }
  if (autoRefreshInterval.value > 0) {
    autoRefreshTimer = setInterval(() => {
      loadData()
    }, autoRefreshInterval.value)
  }
}

watch(autoRefreshInterval, () => {
  setupAutoRefresh()
})

onMounted(() => {
  loadData()
  setupAutoRefresh()
})

onUnmounted(() => {
  if (autoRefreshTimer) clearInterval(autoRefreshTimer)
  if (toastTimer) clearTimeout(toastTimer)
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
  <div class="min-h-screen bg-zinc-950 text-zinc-100 font-sans selection:bg-cyan-500/20 selection:text-cyan-200">
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
    <div class="lg:pl-64 flex flex-col min-h-screen">
      <!-- Top Bar -->
      <TopBar
        :activeTab="activeTab"
        :cluster="cluster"
        :loading="loading"
        :autoRefreshInterval="autoRefreshInterval"
        @toggleMobile="mobileOpen = !mobileOpen"
        @refresh="loadData"
        @update:autoRefreshInterval="autoRefreshInterval = $event"
      />

      <!-- Main Content Container -->
      <main class="flex-1 p-4 sm:p-6 lg:p-8 max-w-7xl w-full mx-auto space-y-6">
        <!-- Mock Data Notice Banner if backend API is not running -->
        <div
          v-if="isMock"
          class="flex items-center justify-between px-4 py-2.5 rounded-xl bg-cyan-950/40 border border-cyan-500/30 text-cyan-300 text-xs shadow-sm"
        >
          <div class="flex items-center gap-2.5">
            <Info class="w-4 h-4 text-cyan-400 shrink-0" />
            <span>{{ t('demo_mode_notice') }}</span>
          </div>
          <span class="font-mono text-[11px] text-zinc-400 hidden sm:inline">
            Proxy: /api -> :8080
          </span>
        </div>

        <!-- Tab 1: Nodes View -->
        <div v-show="activeTab === 'nodes'" class="space-y-6">
          <!-- Cluster Statistics Summary -->
          <ClusterStats :cluster="cluster" />

          <!-- Nodes View Section -->
          <NodesView
            :nodes="nodes"
            @open-services="onOpenServices"
            @open-logs="onOpenLogs"
            @open-reboot="onOpenReboot"
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
      <footer class="border-t border-zinc-800/80 bg-zinc-950/60 py-4 px-4 sm:px-6 lg:px-8 text-xs text-zinc-500 font-medium mt-auto">
        <div class="max-w-7xl mx-auto flex flex-col sm:flex-row items-center justify-between gap-3">
          <div class="flex items-center gap-2">
            <span class="font-bold text-zinc-300">TalosDeck v0.2</span>
            <span>•</span>
            <span class="text-zinc-400">Open-Source Talos Linux Control Plane</span>
          </div>
          <div class="flex flex-wrap items-center gap-4 text-zinc-400">
            <a
              href="https://github.com/etosheartem/TalosDeck"
              target="_blank"
              rel="noopener noreferrer"
              class="hover:text-cyan-400 flex items-center gap-1.5 transition-colors group px-2.5 py-1 rounded-md bg-zinc-900 border border-zinc-800 hover:border-cyan-500/50 text-zinc-300"
            >
              <svg class="w-3.5 h-3.5 fill-current text-zinc-400 group-hover:text-cyan-400" viewBox="0 0 24 24">
                <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.484 2 12.017c0 4.425 2.865 8.18 6.839 9.504.5.092.682-.217.682-.483 0-.237-.008-.868-.013-1.703-2.782.605-3.369-1.343-3.369-1.343-.454-1.158-1.11-1.466-1.11-1.466-.908-.62.069-.608.069-.608 1.003.07 1.53 1.032 1.53 1.032.892 1.53 2.341 1.088 2.91.832.092-.647.35-1.088.636-1.338-2.22-.253-4.555-1.113-4.555-4.951 0-1.093.39-1.988 1.029-2.688-.103-.253-.446-1.272.098-2.65 0 0 .84-.27 2.75 1.026A9.564 9.564 0 0112 6.844c.85.004 1.705.115 2.504.337 1.909-1.296 2.747-1.027 2.747-1.027.546 1.379.202 2.398.1 2.651.64.7 1.028 1.595 1.028 2.688 0 3.848-2.339 4.695-4.566 4.943.359.309.678.92.678 1.855 0 1.338-.012 2.419-.012 2.747 0 .268.18.58.688.482A10.019 10.019 0 0022 12.017C22 6.484 17.522 2 12 2z"/>
              </svg>
              <span class="font-mono text-[11px]">etosheartem/TalosDeck</span>
              <ExternalLink class="w-3 h-3 text-zinc-500 group-hover:text-cyan-400" />
            </a>
            <a
              href="https://www.talos.dev/latest/introduction/what-is-talos/"
              target="_blank"
              rel="noopener noreferrer"
              class="hover:text-cyan-400 flex items-center gap-1 transition-colors"
            >
              <span>Talos Docs</span>
              <ExternalLink class="w-3 h-3" />
            </a>
            <a
              href="https://kubernetes.io"
              target="_blank"
              rel="noopener noreferrer"
              class="hover:text-emerald-400 flex items-center gap-1 transition-colors"
            >
              <span>Kubernetes</span>
              <ExternalLink class="w-3 h-3" />
            </a>
          </div>
        </div>
      </footer>
    </div>
  </div>
</template>

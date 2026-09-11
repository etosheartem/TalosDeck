<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import {
  Activity,
  Server,
  Boxes,
  HardDrive,
  FileCode2,
  Database,
  Archive,
  Wrench,
  ScrollText,
  Settings,
  ChevronRight,
  RefreshCw,
  Menu,
  X,
  LogIn,
  LogOut,
  ArrowUpRight,
  CheckCircle2,
  AlertTriangle,
  Plus,
  Search,
  Network,
} from "lucide-vue-next";
import {
  login,
  logout,
  getMe,
  isAuthenticated,
  currentUser,
  createBackup,
  downloadBackup,
  deleteBackup,
  rebootNode,
  waitForNodeReboot,
  runBootstrapCheck,
} from "../api";
import type {
  NodeOverview,
  ClusterInfo,
  K8sPod,
  EtcdClusterHealth,
  ProxmoxStatusResponse,
} from "../types";
import { request, post, list, bytes, download, normalizeDisk } from "./client";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
import NodeInspector from "./NodeInspector.vue";

const pages = [
  {
    id: "overview",
    title: "Обзор",
    icon: Activity,
    group: "КЛАСТЕР",
    description: "Доступность, ресурсы и состояние инфраструктуры.",
  },
  {
    id: "nodes",
    title: "Ноды",
    icon: Server,
    group: "КЛАСТЕР",
    description:
      "Машины Talos Linux. Выберите ноду для просмотра сервисов и логов.",
  },
  {
    id: "workloads",
    title: "Рабочие нагрузки",
    icon: Boxes,
    group: "КЛАСТЕР",
    description: "Поды Kubernetes во всех пространствах имён.",
  },
  {
    id: "storage",
    title: "Хранилище",
    icon: HardDrive,
    group: "КЛАСТЕР",
    description: "Диски, разделы и точки монтирования на каждой машине.",
  },
  {
    id: "config",
    title: "Конфигурация",
    icon: FileCode2,
    group: "УПРАВЛЕНИЕ",
    description: "Просмотр и экспорт MachineConfig. Доступ только для чтения.",
  },
  {
    id: "etcd",
    title: "etcd",
    icon: Database,
    group: "УПРАВЛЕНИЕ",
    description: "Участники, лидер и состояние распределённой базы данных.",
  },
  {
    id: "backups",
    title: "Резервные копии",
    icon: Archive,
    group: "УПРАВЛЕНИЕ",
    description: "Снимки etcd и полные резервные копии кластера.",
  },
  {
    id: "maintenance",
    title: "Обслуживание",
    icon: Wrench,
    group: "УПРАВЛЕНИЕ",
    description:
      "Диагностика, обслуживание машин и последовательная перезагрузка.",
  },
  {
    id: "audit",
    title: "Аудит",
    icon: ScrollText,
    group: "СИСТЕМА",
    description: "История действий и результаты операций.",
  },
  {
    id: "settings",
    title: "Настройки",
    icon: Settings,
    group: "СИСТЕМА",
    description: "Интеграции, уведомления и параметры опроса.",
  },
];
const initial = () =>
  pages.some((p) => p.id === location.hash.slice(1))
    ? location.hash.slice(1)
    : "overview";
const active = ref(initial());
const page = computed(() => pages.find((p) => p.id === active.value)!);
const mobile = ref(false);
const navSearch = ref("");
const interval = ref(30);
const nodes = ref<NodeOverview[]>([]);
const cluster = ref<ClusterInfo | null>(null);
const pods = ref<K8sPod[]>([]);
const etcd = ref<EtcdClusterHealth | null>(null);
const pve = ref<ProxmoxStatusResponse | null>(null);
const errors = ref<Record<string, string>>({});
const loading = ref(false);
const refreshed = ref("");
const data = ref<any[]>([]);
const sectionLoading = ref(false);
const nodeIP = ref("");
const namespace = ref("all");
const role = ref("all");
const config = ref("");
const configQuery = ref("");
const detail = ref<any>(null);
const inspected = ref<NodeOverview | null>(null);
const dialog = ref("");
const password = ref("");
const busy = ref(false);
const actionError = ref("");
const toast = ref("");
const confirmation = ref<{
  title: string;
  description: string;
  run: () => Promise<unknown>;
} | null>(null);
let toastTimer: ReturnType<typeof setTimeout>;
let timer: ReturnType<typeof setTimeout> | null = null;
let disposed = false;
let generation = 0;
const alerts = ref<any>(null);
const enabled = ref(false);
const chat = ref("");
const token = ref("");
const level = ref("WARNING");
const checks = ref<any[]>([]);
const worker = ref({
  name: "",
  vmid: undefined as number | undefined,
  cores: 2,
  memoryMB: 4096,
  diskGB: 30,
  storage: "local-lvm",
  bridge: "vmbr0",
  iso: "",
  start: true,
});
const rolling = ref("");
const backupType = ref<"full" | "etcd">("etcd");
const ready = computed(() => nodes.value.filter((n) => n.ready).length);
const troubled = computed(() =>
  pods.value.filter((p) => !["Running", "Succeeded"].includes(p.status)),
);
const nodeRows = computed(() =>
  nodes.value
    .filter((n) => role.value === "all" || n.role === role.value)
    .map((n) => ({
      ...n,
      status: n.ready ? "Ready" : "Not Ready",
      cpu: n.cpuUsage == null ? "—" : `${n.cpuUsage.toFixed(1)}%`,
      memory: n.memoryUsage || "—",
    })),
);
const podRows = computed(() =>
  pods.value
    .filter((p) => namespace.value === "all" || p.namespace === namespace.value)
    .map((p) => ({
      ...p,
      nodeName: p.nodeName || p.node,
      ip: p.ip || p.podIp,
    })),
);
const namespaces = computed(() =>
  [...new Set(pods.value.map((p) => p.namespace))].sort(),
);
const issueRows = computed(() => [
  ...nodes.value
    .filter((n) => !n.ready)
    .map((n) => ({
      name: n.hostname,
      reason: "Нода не готова",
      kind: "nodes",
    })),
  ...nodes.value.flatMap((n) =>
    Object.entries(n.servicesSummary || {})
      .filter(([, v]) => v === "Degraded")
      .map(([service]) => ({
        name: `${n.hostname} / ${service}`,
        reason: "Сервис деградирован",
        kind: "nodes",
      })),
  ),
  ...troubled.value.map((p) => ({
    name: p.name,
    reason: p.status,
    kind: "workloads",
  })),
  ...(etcd.value && !etcd.value.healthy
    ? [
        {
          name: "etcd",
          reason: "Проблема кворума или участников",
          kind: "etcd",
        },
      ]
    : []),
]);
const nodeColumns = [
  { key: "hostname", title: "Имя", mono: true },
  { key: "status", title: "Состояние" },
  { key: "role", title: "Роль" },
  { key: "ip", title: "Адрес", mono: true },
  { key: "cpu", title: "CPU", mono: true },
  { key: "memory", title: "Память", mono: true },
  { key: "version", title: "Talos", mono: true },
  { key: "uptime", title: "Время работы", mono: true },
];
const copyConfig = () => navigator.clipboard.writeText(config.value);
function notify(message: string) {
  toast.value = message;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toast.value = ""), 6000);
}
async function perform(
  run: () => Promise<unknown>,
  message = "Операция выполнена",
) {
  if (busy.value) return;
  busy.value = true;
  actionError.value = "";
  try {
    await run();
    notify(message);
    return true;
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e);
    return false;
  } finally {
    busy.value = false;
  }
}
function ask(title: string, description: string, run: () => Promise<unknown>) {
  actionError.value = "";
  confirmation.value = { title, description, run };
}
async function confirm() {
  const c = confirmation.value;
  if (c && (await perform(c.run))) {
    confirmation.value = null;
    await loadSection();
  }
}
async function probe(key: string, path: string, set: (data: any) => void) {
  try {
    const result = await request(path);
    if (!disposed) {
      set(result);
      delete errors.value[key];
    }
  } catch (e) {
    if (!disposed)
      errors.value[key] = e instanceof Error ? e.message : String(e);
  }
}
async function refresh() {
  if (loading.value) return;
  loading.value = true;
  await Promise.allSettled([
    probe("nodes", "/nodes", (v) => {
      nodes.value = list(v);
      if (!nodes.value.some((n) => n.ip === nodeIP.value))
        nodeIP.value = nodes.value[0]?.ip || "";
    }),
    probe("cluster", "/cluster", (v) => (cluster.value = v)),
    probe("pods", "/k8s/pods", (v) => (pods.value = list(v))),
    probe(
      "etcd",
      "/cluster/etcd",
      (v) =>
        (etcd.value = {
          ...v,
          members: (v.members || []).map((m: any) => ({
            ...m,
            peerURLs: m.peerURLs || m.peerUrls,
            clientURLs: m.clientURLs || m.clientUrls,
          })),
        }),
    ),
    probe("proxmox", "/proxmox/status", (v) => (pve.value = v)),
  ]);
  if (!disposed) {
    refreshed.value = new Date().toLocaleTimeString("ru-RU");
    loading.value = false;
  }
}
function schedule() {
  if (timer) clearTimeout(timer);
  if (interval.value && !disposed)
    timer = setTimeout(async () => {
      await refresh();
      if (["storage", "config", "backups", "audit"].includes(active.value))
        await loadSection();
      schedule();
    }, interval.value * 1000);
}
async function loadSection() {
  const id = ++generation;
  const section = active.value;
  data.value = [];
  config.value = "";
  detail.value = null;
  delete errors.value.section;
  sectionLoading.value = true;
  try {
    let result: any;
    if (section === "storage") {
      const targets = nodeIP.value
        ? nodes.value.filter((n) => n.ip === nodeIP.value)
        : nodes.value;
      const parts = await Promise.allSettled(
        targets.map(async (n) =>
          list(await request(`/nodes/${encodeURIComponent(n.ip)}/disks`)).map(
            (d) => ({ ...normalizeDisk(d), node: n.hostname }),
          ),
        ),
      );
      result = parts.flatMap((p) => (p.status === "fulfilled" ? p.value : []));
      if (parts.some((p) => p.status === "rejected"))
        throw new Error("Не удалось получить диски выбранной ноды");
    }
    if (section === "config" && nodeIP.value) {
      const r = await request(
        `/nodes/${encodeURIComponent(nodeIP.value)}/config`,
      );
      if (id === generation) config.value = r.configYaml || r.yaml || "";
    }
    if (section === "backups") result = await request("/backups");
    if (section === "audit") result = list(await request("/audit?limit=100"));
    if (section === "settings") {
      const a = await request("/alerts/config");
      if (id === generation) {
        alerts.value = a;
        enabled.value = a.enabled;
        chat.value = a.chat_id || a.chatID || "";
        level.value = a.min_level || a.minLevel || "WARNING";
      }
    }
    if (id === generation && result) data.value = list(result);
  } catch (e) {
    if (id === generation)
      errors.value.section = e instanceof Error ? e.message : String(e);
  } finally {
    if (id === generation) sectionLoading.value = false;
  }
}
function navigate(id: string) {
  active.value = id;
  mobile.value = false;
  location.hash = id;
}
const hash = () => {
  active.value = initial();
};
watch(active, () => {
  loadSection();
});
watch(nodeIP, () => {
  if (["storage", "config"].includes(active.value)) loadSection();
});
watch(interval, schedule);
watch(isAuthenticated, (authorized) => {
  if (!authorized) {
    generation++;
    config.value = "";
    data.value = [];
    alerts.value = null;
    detail.value = null;
    inspected.value = null;
    sectionLoading.value = false;
  }
});
onMounted(async () => {
  window.addEventListener("hashchange", hash);
  await getMe();
  await refresh();
  await loadSection();
  schedule();
});
onUnmounted(() => {
  disposed = true;
  generation++;
  if (timer) clearTimeout(timer);
  clearTimeout(toastTimer);
  window.removeEventListener("hashchange", hash);
});
async function signIn() {
  if (
    await perform(async () => {
      const r = await login(password.value);
      if (!r.success) throw new Error(r.error);
    }, "Вход выполнен")
  ) {
    dialog.value = "";
    password.value = "";
    await loadSection();
  }
}
function showWorker() {
  actionError.value = "";
  dialog.value = "worker";
  probe(
    "vmid",
    "/proxmox/next-vmid",
    (v) => (worker.value.vmid = v.vmid || v.nextVMID),
  );
}
async function createWorker() {
  if (
    await perform(
      () => post("/proxmox/worker", worker.value),
      "Рабочая машина создана",
    )
  ) {
    dialog.value = "";
    await refresh();
  }
}
async function saveAlerts() {
  await perform(
    () =>
      post("/alerts/config", {
        enabled: enabled.value,
        min_level: level.value,
        ...(chat.value && !chat.value.includes("*")
          ? { chat_id: chat.value }
          : {}),
        ...(token.value ? { bot_token: token.value } : {}),
      }),
    "Настройки сохранены",
  );
  token.value = "";
}
async function rollingReboot() {
  for (const n of [
    ...nodes.value.filter((n) => n.role === "worker"),
    ...nodes.value.filter((n) => n.role !== "worker"),
  ]) {
    rolling.value = `Перезагрузка ${n.hostname}`;
    await rebootNode(n.ip);
    await waitForNodeReboot(n.ip);
  }
  rolling.value = "";
  await refresh();
}
const protectedPage = computed(
  () =>
    ["config", "backups", "audit", "settings"].includes(active.value) &&
    !isAuthenticated.value,
);
</script>

<template>
  <div class="console-app">
    <aside :class="['rail', { open: mobile }]">
      <a class="brand" href="#overview" @click.prevent="navigate('overview')"
        ><span class="brand-symbol">t<span>_</span></span>
        <div>TalosDeck<small>INFRASTRUCTURE CONSOLE</small></div></a
      ><button
        class="mobile-close icon-button"
        aria-label="Закрыть навигацию"
        @click="mobile = false"
      >
        <X :size="20" />
      </button>
      <label class="nav-search"
        ><Search :size="15" /><input
          v-model="navSearch"
          placeholder="Найти раздел"
          aria-label="Найти раздел"
      /></label>
      <nav aria-label="Главная навигация">
        <template
          v-for="group in ['КЛАСТЕР', 'УПРАВЛЕНИЕ', 'СИСТЕМА']"
          :key="group"
          ><p class="nav-group">{{ group }}</p>
          <button
            v-for="item in pages.filter(
              (p) =>
                p.group === group &&
                p.title.toLowerCase().includes(navSearch.toLowerCase()),
            )"
            :key="item.id"
            :class="{ active: active === item.id }"
            :aria-current="active === item.id ? 'page' : undefined"
            @click="navigate(item.id)"
          >
            <component :is="item.icon" :size="17" /><span>{{ item.title }}</span
            ><span v-if="item.id === 'nodes'" class="nav-count">{{
              nodes.length
            }}</span>
          </button></template
        >
      </nav>
      <div class="rail-footer">
        <span class="state muted"><i />Console rebuild</span
        ><code>UI 2 · operator-console</code
        ><a href="https://www.talos.dev" target="_blank" rel="noopener"
          >Документация Talos <ArrowUpRight :size="13"
        /></a>
      </div>
    </aside>
    <button
      v-if="mobile"
      class="nav-backdrop"
      aria-label="Закрыть меню"
      @click="mobile = false"
    />
    <div class="workspace">
      <header class="topline">
        <button
          class="mobile-menu icon-button"
          aria-label="Открыть меню"
          @click="mobile = true"
        >
          <Menu :size="20" />
        </button>
        <div class="breadcrumbs">
          <Network :size="16" /><span>{{ cluster?.name || "Кластер" }}</span
          ><ChevronRight :size="14" /><strong>{{ page.title }}</strong>
        </div>
        <div class="session">
          <span class="viewer-label">{{
            isAuthenticated ? currentUser.username : "Только просмотр"
          }}</span
          ><button
            v-if="!isAuthenticated"
            @click="
              dialog = 'login';
              actionError = '';
            "
          >
            <LogIn :size="15" />Войти</button
          ><button
            v-else
            @click="
              logout().then(() => {
                config = '';
                data = [];
                alerts = null;
                notify('Вы вышли');
              })
            "
          >
            <LogOut :size="15" />Выйти
          </button>
        </div>
      </header>
      <main>
        <div class="page-heading">
          <div>
            <div class="eyebrow">{{ page.group }} / TALOS LINUX</div>
            <h1>{{ page.title }}</h1>
            <p>{{ page.description }}</p>
          </div>
          <div class="refresh-tools">
            <span v-if="refreshed">Опрос {{ refreshed }}</span
            ><select v-model="interval" aria-label="Интервал обновления">
              <option :value="0">Вручную</option>
              <option :value="10">10 сек</option>
              <option :value="30">30 сек</option>
              <option :value="60">1 мин</option></select
            ><button
              :disabled="loading || sectionLoading"
              @click="refresh().then(loadSection)"
            >
              <RefreshCw :size="15" :class="{ spin: loading }" />Обновить
            </button>
          </div>
        </div>
        <div
          v-if="Object.keys(errors).filter((k) => k !== 'section').length"
          class="notice warning"
          role="status"
        >
          <AlertTriangle :size="18" />
          <div>
            <strong>Часть данных недоступна</strong>
            <p>Последние полученные значения могут быть устаревшими.</p>
            <details>
              <summary>Подробности</summary>
              <p v-for="(message, key) in errors" :key="key">
                {{ key }}: {{ message }}
              </p>
            </details>
          </div>
        </div>
        <div v-if="loading && !refreshed" class="loading-state">
          <RefreshCw :size="22" class="spin" />Подключение к кластеру…
        </div>
        <template v-if="active === 'overview'">
          <div class="overview-grid">
            <section class="availability">
              <div class="section-label">ДОСТУПНОСТЬ КЛАСТЕРА</div>
              <div class="availability-value">
                <span>{{ refreshed && !errors.nodes ? ready : "—" }}</span
                ><small>/ {{ nodes.length || "—" }} нод</small>
              </div>
              <p>
                <span
                  :class="[
                    'state',
                    errors.nodes
                      ? 'muted'
                      : ready === nodes.length && ready > 0
                        ? 'good'
                        : 'bad',
                  ]"
                  ><i />{{
                    errors.nodes
                      ? "Нет актуальных данных"
                      : nodes.length
                        ? ready === nodes.length
                          ? "Все ноды готовы"
                          : "Требует внимания"
                        : "Ожидание данных"
                  }}</span
                >
              </p>
              <div class="fleet-bars">
                <span
                  v-for="n in nodes"
                  :key="n.ip"
                  :class="{ healthy: n.ready }"
                  :title="n.hostname"
                />
              </div>
              <button class="text-link" @click="navigate('nodes')">
                Открыть список нод <ArrowUpRight :size="16" />
              </button>
            </section>
            <section class="overview-metrics">
              <div>
                <span>Control plane</span
                ><strong>{{
                  nodes.filter((n) => n.role === "controlplane").length
                }}</strong
                ><small>Управление кластером</small>
              </div>
              <div>
                <span>Workers</span
                ><strong>{{
                  nodes.filter((n) => n.role === "worker").length
                }}</strong
                ><small>Вычислительные ноды</small>
              </div>
              <div>
                <span>Поды</span
                ><strong>{{ errors.pods ? "—" : pods.length }}</strong
                ><small>{{ troubled.length }} требуют внимания</small>
              </div>
              <div>
                <span>etcd</span
                ><strong :class="etcd?.healthy ? 'text-good' : ''">{{
                  errors.etcd
                    ? "—"
                    : etcd?.healthy
                      ? "Healthy"
                      : etcd
                        ? "Degraded"
                        : "—"
                }}</strong
                ><small>Участники: {{ etcd?.members?.length ?? "—" }}</small>
              </div>
            </section>
          </div>
          <div class="two-columns">
            <section class="surface">
              <header class="surface-heading">
                <h2>Ресурсы машин</h2>
                <span>Текущий срез</span>
              </header>
              <div v-for="n in nodes" :key="n.ip" class="resource-meter">
                <button class="resource-link" @click="inspected = n">
                  {{ n.hostname }}
                </button>
                <div>
                  <span>CPU</span>
                  <div class="bar">
                    <i
                      :style="{ width: `${Math.min(100, n.cpuUsage || 0)}%` }"
                    />
                  </div>
                  <code>{{ n.cpuUsage?.toFixed(1) ?? "—" }}%</code>
                </div>
                <small class="mono">RAM {{ n.memoryUsage || "—" }}</small>
              </div>
              <div v-if="!nodes.length" class="empty-state">
                Нет данных о машинах
              </div>
            </section>
            <section class="surface">
              <header class="surface-heading">
                <h2>Требует внимания</h2>
                <span>{{ issueRows.length }}</span>
              </header>
              <div v-if="!issueRows.length" class="quiet-state">
                <CheckCircle2 :size="28" /><strong>{{
                  Object.keys(errors).length
                    ? "Проверка неполная"
                    : "Активных проблем не обнаружено"
                }}</strong>
                <p>
                  {{
                    Object.keys(errors).length
                      ? "Часть источников не ответила."
                      : "По последним ответам Talos и Kubernetes."
                  }}
                </p>
              </div>
              <button
                v-for="issue in issueRows.slice(0, 8)"
                :key="issue.name"
                class="issue-row"
                @click="navigate(issue.kind)"
              >
                <AlertTriangle :size="16" /><span
                  >{{ issue.name }}<small>{{ issue.reason }}</small></span
                ><ChevronRight :size="16" />
              </button>
            </section>
          </div>
          <section class="surface cluster-facts">
            <div>
              <span>API endpoint</span
              ><code>{{ cluster?.endpoint || "—" }}</code>
            </div>
            <div>
              <span>Talos Linux</span
              ><code>{{ cluster?.talosVersion || "—" }}</code>
            </div>
            <div>
              <span>Kubernetes</span
              ><code>{{ cluster?.kubernetesVersion || "—" }}</code>
            </div>
            <div>
              <span>Proxmox VE</span
              ><code>{{ pve?.configured ? pve.node : "Не настроен" }}</code>
            </div>
          </section>
        </template>
        <template v-else-if="active === 'nodes'"
          ><div class="toolbar">
            <label
              >Роль
              <select v-model="role">
                <option value="all">Все роли</option>
                <option value="controlplane">Control plane</option>
                <option value="worker">Worker</option>
              </select></label
            ><span class="spacer" /><button
              class="primary"
              :disabled="!isAuthenticated || !pve?.configured"
              @click="showWorker"
            >
              <Plus :size="16" />Добавить worker
            </button>
          </div>
          <ResourceTable
            :rows="nodeRows"
            :columns="nodeColumns"
            @select="inspected = $event"
        /></template>
        <template v-else-if="active === 'workloads'"
          ><div class="toolbar">
            <label
              >Namespace
              <select v-model="namespace">
                <option value="all">Все пространства имён</option>
                <option v-for="ns in namespaces" :key="ns">{{ ns }}</option>
              </select></label
            ><span class="spacer" /><span class="state muted"
              >{{ troubled.length }} требуют внимания</span
            >
          </div>
          <ResourceTable
            :rows="podRows"
            :columns="[
              { key: 'name', title: 'Под', mono: true },
              { key: 'namespace', title: 'Namespace' },
              { key: 'status', title: 'Состояние' },
              { key: 'readyContainers', title: 'Готовность' },
              { key: 'restarts', title: 'Рестарты' },
              { key: 'nodeName', title: 'Нода' },
              { key: 'ip', title: 'IP', mono: true },
              { key: 'age', title: 'Возраст' },
            ]"
            @select="detail = $event"
        /></template>
        <template v-else-if="active === 'etcd'"
          ><div class="cluster-facts surface">
            <div>
              <span>Лидер</span><code>{{ etcd?.leaderName || "—" }}</code>
            </div>
            <div>
              <span>Размер базы</span
              ><code>{{ etcd?.totalDbSize || "—" }}</code>
            </div>
            <div>
              <span>Raft term / index</span
              ><code
                >{{ etcd?.raftTerm ?? "—" }} /
                {{ etcd?.raftIndex ?? "—" }}</code
              >
            </div>
          </div>
          <div v-if="etcd?.alarms?.length" class="notice error">
            {{ etcd.alarms }}
          </div>
          <ResourceTable
            :rows="
              (etcd?.members || []).map((m) => ({
                ...m,
                status: m.healthy ? 'Healthy' : 'Degraded',
                role: m.leader ? 'Leader' : 'Follower',
              }))
            "
            :columns="[
              { key: 'name', title: 'Участник' },
              { key: 'status', title: 'Состояние' },
              { key: 'role', title: 'Роль' },
              { key: 'dbSize', title: 'Размер БД' },
              { key: 'peerURLs', title: 'Peer URLs', mono: true },
            ]"
            @select="detail = $event"
        /></template>
        <div v-else-if="protectedPage" class="access-state">
          <LogIn :size="28" />
          <h2>Требуется вход</h2>
          <p>Раздел «{{ page.title }}» доступен администратору.</p>
          <button
            class="primary"
            @click="
              dialog = 'login';
              actionError = '';
            "
          >
            Войти
          </button>
        </div>
        <template v-else>
          <div
            v-if="['storage', 'config', 'maintenance'].includes(active)"
            class="toolbar"
          >
            <label
              >Машина
              <select v-model="nodeIP">
                <option v-if="!nodes.length" value="">Нет доступных нод</option>
                <option v-for="n in nodes" :key="n.ip" :value="n.ip">
                  {{ n.hostname }} · {{ n.ip }}
                </option>
              </select></label
            >
          </div>
          <div v-if="errors.section" class="notice error" role="alert">
            {{ errors.section }}<button @click="loadSection">Повторить</button>
          </div>
          <div v-if="sectionLoading" class="loading-state">
            <RefreshCw :size="20" class="spin" />Загрузка раздела…
          </div>
          <template v-else-if="active === 'storage'"
            ><ResourceTable
              :rows="data"
              :columns="[
                { key: 'name', title: 'Устройство', mono: true },
                { key: 'node', title: 'Нода' },
                { key: 'model', title: 'Модель' },
                { key: 'size', title: 'Размер' },
                { key: 'type', title: 'Тип' },
                { key: 'status', title: 'Здоровье' },
                { key: 'bus', title: 'Шина' },
              ]"
              @select="detail = $event"
            />
            <p class="footnote">
              Выберите диск, чтобы увидеть разделы, файловые системы и точки
              монтирования.
            </p></template
          >
          <template v-else-if="active === 'config'"
            ><section class="code-surface">
              <header class="surface-heading">
                <h2>
                  machineconfig.yaml <span class="state muted">Read only</span>
                </h2>
                <div class="toolbar">
                  <button
                    :disabled="!config"
                    @click="perform(() => copyConfig(), 'Скопировано')"
                  >
                    Копировать</button
                  ><button
                    :disabled="!config"
                    @click="download(config, `${nodeIP}-machineconfig.yaml`)"
                  >
                    Скачать YAML
                  </button>
                </div>
              </header>
              <label class="search-field"
                ><Search :size="16" /><input
                  v-model="configQuery"
                  placeholder="Найти в конфигурации"
              /></label>
              <div class="code-lines">
                <div
                  v-for="(line, i) in config.split('\n')"
                  :key="i"
                  :class="{
                    match:
                      configQuery &&
                      line.toLowerCase().includes(configQuery.toLowerCase()),
                  }"
                >
                  <span>{{ i + 1 }}</span
                  ><code>{{ line }}</code>
                </div>
                <p v-if="!config">
                  {{
                    errors.section
                      ? "Конфигурация недоступна."
                      : "Выберите ноду для просмотра конфигурации."
                  }}
                </p>
              </div>
              <footer>Закрытые ключи маскируются сервером.</footer>
            </section></template
          >
          <template v-else-if="active === 'backups'"
            ><div class="toolbar">
              <select v-model="backupType" aria-label="Тип резервной копии">
                <option value="etcd">Снимок etcd</option>
                <option value="full">Полная копия</option></select
              ><button
                class="primary"
                :disabled="busy"
                @click="
                  perform(
                    () => createBackup(backupType),
                    'Резервная копия создана',
                  ).then(loadSection)
                "
              >
                <Plus :size="16" />Создать копию
              </button>
            </div>
            <ResourceTable
              :rows="data"
              :columns="[
                { key: 'filename', title: 'Файл', mono: true },
                { key: 'type', title: 'Тип' },
                { key: 'humanSize', title: 'Размер' },
                { key: 'timestamp', title: 'Создано' },
                { key: 'node', title: 'Нода' },
              ]"
              @select="detail = $event"
          /></template>
          <template v-else-if="active === 'audit'"
            ><ResourceTable
              :rows="data"
              :columns="[
                { key: 'action', title: 'Действие' },
                { key: 'user', title: 'Пользователь' },
                { key: 'status', title: 'Результат' },
                { key: 'ip', title: 'Адрес', mono: true },
                { key: 'timestamp', title: 'Время' },
              ]"
              @select="detail = $event"
          /></template>
          <template v-else-if="active === 'maintenance'"
            ><section class="surface operation-list">
              <article>
                <div>
                  <h2>Проверка кластера</h2>
                  <p>
                    Проверить доступность API, системные компоненты и рабочие
                    нагрузки.
                  </p>
                </div>
                <button
                  :disabled="busy"
                  @click="
                    perform(async () => {
                      checks = await runBootstrapCheck();
                    }, 'Проверка завершена')
                  "
                >
                  Запустить проверку
                </button>
              </article>
              <article>
                <div>
                  <h2>Режим обслуживания</h2>
                  <p>Управление режимом обслуживания выбранной ноды.</p>
                </div>
                <div class="toolbar">
                  <button
                    :disabled="!isAuthenticated || !nodeIP || busy"
                    @click="
                      ask(
                        'Включить обслуживание',
                        `Нода ${nodeIP} будет переведена в режим обслуживания.`,
                        () =>
                          post(`/nodes/${nodeIP}/maintenance`, {
                            enable: true,
                          }),
                      )
                    "
                  >
                    Включить</button
                  ><button
                    :disabled="!isAuthenticated || !nodeIP || busy"
                    @click="
                      ask(
                        'Выключить обслуживание',
                        `Выйти из режима обслуживания на ${nodeIP}.`,
                        () =>
                          post(`/nodes/${nodeIP}/maintenance`, {
                            enable: false,
                          }),
                      )
                    "
                  >
                    Выключить
                  </button>
                </div>
              </article>
              <article>
                <div>
                  <h2>Перезагрузить ноду</h2>
                  <p>
                    Рабочие нагрузки на {{ nodeIP || "выбранной ноде" }} будут
                    прерваны.
                  </p>
                </div>
                <button
                  class="danger"
                  :disabled="!isAuthenticated || !nodeIP || busy"
                  @click="
                    ask(
                      'Перезагрузить ноду',
                      `Подтвердите перезагрузку ${nodeIP}.`,
                      () => rebootNode(nodeIP),
                    )
                  "
                >
                  Перезагрузить
                </button>
              </article>
              <article>
                <div>
                  <h2>Последовательная перезагрузка</h2>
                  <p>
                    Сначала workers, затем control plane. Ожидание готовности
                    каждой ноды.
                  </p>
                </div>
                <button
                  class="danger"
                  :disabled="!isAuthenticated || !nodes.length || busy"
                  @click="
                    ask(
                      'Перезагрузить кластер',
                      'Перезагрузить все ноды по очереди? Операция может занять несколько минут.',
                      rollingReboot,
                    )
                  "
                >
                  Перезагрузить все
                </button>
              </article>
            </section>
            <p v-if="rolling">{{ rolling }}</p>
            <ResourceTable
              v-if="checks.length"
              :rows="checks"
              :columns="[
                { key: 'title', title: 'Проверка' },
                { key: 'status', title: 'Результат' },
                { key: 'detail', title: 'Подробности' },
              ]"
              @select="detail = $event"
          /></template>
          <template v-else-if="active === 'settings'"
            ><div class="settings-grid">
              <section class="surface">
                <header class="surface-heading">
                  <h2>Telegram</h2>
                  <span class="state muted">{{
                    alerts?.bot_configured ? "Настроен" : "Не настроен"
                  }}</span>
                </header>
                <form class="settings-form" @submit.prevent="saveAlerts">
                  <label class="check-label"
                    ><input v-model="enabled" type="checkbox" /> Отправлять
                    уведомления</label
                  ><label
                    >Bot token<input
                      v-model="token"
                      type="password"
                      autocomplete="new-password"
                      placeholder="Оставьте пустым, чтобы сохранить текущий" /></label
                  ><label
                    >Chat ID<input
                      v-model="chat"
                      placeholder="Например, -1001234567890" /></label
                  ><label
                    >Минимальный уровень<select v-model="level">
                      <option>INFO</option>
                      <option>WARNING</option>
                      <option>CRITICAL</option>
                    </select></label
                  >
                  <div class="toolbar">
                    <button
                      class="primary"
                      :disabled="busy || !!errors.section"
                    >
                      Сохранить</button
                    ><button
                      type="button"
                      :disabled="busy"
                      @click="
                        perform(
                          () => post('/alerts/test', {}),
                          'Тестовое уведомление отправлено',
                        )
                      "
                    >
                      Отправить тест
                    </button>
                  </div>
                </form>
              </section>
              <section class="surface">
                <header class="surface-heading">
                  <h2>Proxmox VE</h2>
                  <span class="state muted">{{
                    pve?.configured ? "Настроен" : "Не настроен"
                  }}</span>
                </header>
                <dl class="definition-list">
                  <dt>Хост</dt>
                  <dd>{{ pve?.node || "—" }}</dd>
                  <dt>CPU</dt>
                  <dd>
                    {{ pve?.status?.cpuUsagePercent?.toFixed(1) ?? "—" }}%
                  </dd>
                  <dt>Свободная RAM</dt>
                  <dd>{{ bytes(pve?.status?.memory?.available) }}</dd>
                  <dt>Свободный диск</dt>
                  <dd>{{ bytes(pve?.status?.storage?.free) }}</dd>
                </dl>
                <p class="footnote">
                  Подключение Proxmox задаётся в конфигурации сервера.
                </p>
                <button
                  class="settings-action"
                  :disabled="!pve?.configured"
                  @click="showWorker"
                >
                  Добавить рабочую машину
                </button>
              </section>
            </div></template
          >
        </template>
        <div
          v-if="actionError && !dialog && !confirmation && !detail"
          class="notice error"
          role="alert"
        >
          {{ actionError }}
        </div>
      </main>
      <footer class="workspace-footer">
        <span>TalosDeck <strong>Console UI 2</strong></span
        ><span>Talos Linux / Kubernetes</span>
      </footer>
    </div>
    <div v-if="toast" class="toast" role="status">
      <CheckCircle2 :size="18" />{{ toast
      }}<button
        class="icon-button"
        aria-label="Закрыть сообщение"
        @click="toast = ''"
      >
        <X :size="16" />
      </button>
    </div>
    <Modal
      v-if="dialog === 'login'"
      title="Вход администратора"
      @close="dialog = ''"
      ><form class="settings-form" @submit.prevent="signIn">
        <p>Введите пароль администратора TalosDeck.</p>
        <label
          >Пароль<input
            v-model="password"
            type="password"
            autocomplete="current-password"
            required
            autofocus
        /></label>
        <div v-if="actionError" class="notice error" role="alert">
          {{ actionError }}
        </div>
        <button class="primary" :disabled="busy">
          {{ busy ? "Вход…" : "Войти" }}
        </button>
      </form></Modal
    >
    <Modal
      v-if="dialog === 'worker'"
      title="Новая рабочая машина"
      @close="!busy && (dialog = '')"
      ><form class="settings-form" @submit.prevent="createWorker">
        <p>Создать виртуальную машину Talos в Proxmox VE.</p>
        <div class="form-grid">
          <label
            >Имя<input
              v-model="worker.name"
              required
              pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?"
              maxlength="63"
              placeholder="talos-worker-3" /></label
          ><label
            >VMID<input
              v-model.number="worker.vmid"
              type="number"
              min="100"
              max="9999"
              placeholder="Автоматически" /></label
          ><label
            >vCPU<input
              v-model.number="worker.cores"
              type="number"
              min="1"
              max="64"
              required /></label
          ><label
            >RAM, MiB<input
              v-model.number="worker.memoryMB"
              type="number"
              min="512"
              required /></label
          ><label
            >Диск, GiB<input
              v-model.number="worker.diskGB"
              type="number"
              min="10"
              required /></label
          ><label>Storage<input v-model="worker.storage" required /></label
          ><label>Bridge<input v-model="worker.bridge" required /></label
          ><label
            >ISO<input v-model="worker.iso" placeholder="По настройке сервера"
          /></label>
        </div>
        <label class="check-label"
          ><input v-model="worker.start" type="checkbox" /> Запустить после
          создания</label
        >
        <div v-if="actionError" class="notice error">{{ actionError }}</div>
        <button class="primary" :disabled="busy || !isAuthenticated">
          {{ busy ? "Создание…" : "Создать машину" }}
        </button>
      </form></Modal
    >
    <Modal
      v-if="confirmation"
      :title="confirmation.title"
      @close="!busy && (confirmation = null)"
      ><p>{{ confirmation.description }}</p>
      <div v-if="actionError" class="notice error">{{ actionError }}</div>
      <p v-if="rolling">{{ rolling }}</p>
      <div class="dialog-actions">
        <button :disabled="busy" @click="confirmation = null">Отмена</button
        ><button class="danger" :disabled="busy" @click="confirm">
          {{ busy ? "Выполняется…" : "Подтвердить" }}
        </button>
      </div></Modal
    >
    <Modal
      v-if="inspected"
      :title="inspected.hostname"
      wide
      @close="inspected = null"
      ><NodeInspector :node="inspected" @changed="refresh"
    /></Modal>
    <Modal
      v-if="detail"
      :title="
        detail.name ||
        detail.filename ||
        detail.action ||
        detail.title ||
        'Подробности'
      "
      wide
      @close="detail = null"
      ><template v-if="active === 'backups'"
        ><div class="toolbar">
          <button
            :disabled="busy"
            @click="perform(() => downloadBackup(detail), 'Файл скачан')"
          >
            Скачать</button
          ><button
            class="danger"
            :disabled="busy"
            @click="
              ask(
                'Удалить резервную копию',
                `Файл ${detail.filename} будет удалён.`,
                async () => {
                  await deleteBackup(detail.id);
                  detail = null;
                },
              )
            "
          >
            Удалить
          </button>
        </div></template
      ><ResourceTable
        v-if="detail.partitions"
        :search="false"
        :rows="detail.partitions"
        :columns="[
          { key: 'device', title: 'Раздел' },
          { key: 'label', title: 'Метка' },
          { key: 'filesystem', title: 'ФС' },
          { key: 'mountpoint', title: 'Точка монтирования' },
          { key: 'size', title: 'Размер' },
          { key: 'used', title: 'Занято' },
        ]"
        @select="notify($event.device)"
      />
      <div v-if="actionError" class="notice error" role="alert">
        {{ actionError }}
      </div>
      <pre class="detail-json">{{ JSON.stringify(detail, null, 2) }}</pre>
    </Modal>
  </div>
</template>

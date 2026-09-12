<script setup lang="ts">
import { t, locale, setLocale } from "./i18n";
import { recoveryState } from "./recovery";

import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import {
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
  downloadBackup,
  deleteBackup,
  rebootNode,
  runBootstrapCheck,
  TOKEN_STORAGE_KEY,
} from "../api";
import type {
  NodeOverview,
  ClusterInfo,
  K8sPod,
  EtcdClusterHealth,
} from "../types";
import { request, post, list, normalizeDisk } from "./client";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
import NodeInspector from "./NodeInspector.vue";
import JobsView from "./JobsView.vue";
import ConfigView from "./ConfigView.vue";
import SecurityView from "./SecurityView.vue";
import ProvidersView from "./ProvidersView.vue";
import ProvisionView from "./ProvisionView.vue";
import DiagnosticsView from "./DiagnosticsView.vue";
import CertificatesView from "./CertificatesView.vue";
import HealthView from "./HealthView.vue";
import {healthExpired,healthNumber,type HealthReport} from "./health";
const healthReport=ref<HealthReport|null>(null);
import CommandPalette, { type Command } from "./CommandPalette.vue";
import SettingsHub from "./SettingsHub.vue";
import AlertCenter from "./AlertCenter.vue";
import ImagePicker from "./ImagePicker.vue";
import TemplatesView from "./TemplatesView.vue";
import NotificationSettings from "./NotificationSettings.vue";
import { notificationTime, notificationLabel, type AlertSnapshot } from "./notifications";
const notificationStatus = ref<Pick<AlertSnapshot, 'health'|'lastCheckAt'|'summary'> | null>(null);
import { certificateStatus, type CertificateReport } from "./certificates";
const certificates = ref<CertificateReport | null>(null);
import KubernetesView from "./KubernetesView.vue";
import MachinesView from "./MachinesView.vue";
import AuditView from "./AuditView.vue";
import BackupsView from "./BackupsView.vue";
import { navigation } from "./navigation";
import { isAdmin, canOperate } from "./permissions";
import { selectedCluster, selectCluster, clusterEpoch } from "../clusterScope";
const clusters = ref<any[]>([]);
const registryError = ref("");
const importing = ref(false);
const showGlobalJobs = ref(false);
const importForm = ref({ name: "", talosconfig: "", kubeconfig: "" });
async function loadClusters() {
  if (!isAuthenticated.value) return;
  try {
    const result = await request("/clusters");
    clusters.value = result.clusters || [];
    registryError.value = "";
    if (!clusters.value.some((c) => c.id === selectedCluster.value))
      selectCluster(clusters.value[0]?.id || "");
  } catch (e) {
    registryError.value = e instanceof Error ? e.message : String(e);
  }
}
async function importCluster() {
  if (importing.value) return;
  importing.value = true;
  actionError.value = "";
  try {
    const result = await post("/clusters", importForm.value);
    importForm.value = { name: "", talosconfig: "", kubeconfig: "" };
    dialog.value = "";
    await loadClusters();
    switchCluster(result.cluster.id);
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    importing.value = false;
  }
}

async function readImportFile(event: Event, field: "talosconfig" | "kubeconfig") {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  const form = importForm.value;
  if (!file) return;
  actionError.value = "";
  try {
    if (file.size > 2 * 1024 * 1024) throw new Error(t("Файл слишком большой. Максимум 2 МиБ."));
    const value = await file.text();
    if (dialog.value === "import" && importForm.value === form && input.files?.[0] === file && !importing.value) form[field] = value;
  } catch (e) {
    if (dialog.value === "import" && importForm.value === form && input.files?.[0] === file) actionError.value = e instanceof Error ? e.message : t("Не удалось прочитать файл");
  } finally { if (input.files?.[0] === file) input.value = ""; }
}

const pages = computed(navigation);
const globalPage = computed(() => Boolean(page.value.global));
const navGroups = computed(() => [
  ...new Set(pages.value.map((item) => item.group)),
]);
const expandedGroups = ref<string[]>([]);
function toggleGroup(group: string) {
  expandedGroups.value = expandedGroups.value.includes(group)
    ? expandedGroups.value.filter((g) => g !== group)
    : [group];
}
const initial = () =>
  pages.value.some((p) => p.id === location.hash.slice(1))
    ? location.hash.slice(1)
    : "clusters";
const active = ref(initial());
const page = computed(() => pages.value.find((p) => p.id === active.value)!);
const mobile = ref(false);
const navSearch = ref("");
const paletteOpen = ref(false);
async function shortcut(event:KeyboardEvent) {if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==='k'){event.preventDefault();paletteOpen.value=!paletteOpen.value;} }
const interval = ref(30);
const nodes = ref<NodeOverview[]>([]);
const cluster = ref<ClusterInfo | null>(null);
const pods = ref<K8sPod[]>([]);
const recentJobs = ref<any[]>([]);
const recentBackups = ref<any[]>([]);
const diagnostics = ref<any>(null);
const impactedPods = computed(() => {const node=nodes.value.find(n=>n.ip===nodeIP.value);return pods.value.filter(p=>[node?.hostname,nodeIP.value].includes(p.nodeName || p.node)).length;});
const etcd = ref<EtcdClusterHealth | null>(null);
const errors = ref<Record<string, string>>({});
const loading = ref(false);
const refreshed = ref("");
const data = ref<any[]>([]);
const sectionLoading = ref(false);
const nodeIP = ref("");
const role = ref("all");
const config = ref("");
const detail = ref<any>(null);
const inspected = ref<NodeOverview | null>(null);
const dialog = ref("");
const password = ref("");
const username = ref("admin");
const oidc = ref<any>(null);
const busy = ref(false);
const actionError = ref("");
const toast = ref("");
const logoutWarning = ref('');
async function signOut() {
 const result = await logout();
 config.value=''; data.value=[];  paletteOpen.value=false;
 inspected.value=null;detail.value=null;confirmation.value=null;dialog.value='';importForm.value={name:'',talosconfig:'',kubeconfig:''};password.value='';workloadFocus.value=null;
 logoutWarning.value=result.revoked?'':t('Локальный выход выполнен, но отзыв сеанса на сервере не подтверждён. При восстановлении связи отзовите сеансы через администратора.');
 if(result.revoked) notify(t('Вы вышли'));
}
const commands = computed<Command[]>(() => {
 const scope=clusters.value.find(c=>c.id===selectedCluster.value)?.name || t('Платформа');
 const allowed=(id:string)=>!(['providers','config','alerts','machines','fleet-machines'].includes(id)&&!isAdmin.value)&&!(id==='updates'&&!canOperate.value);
 const all:Command[]=pages.value.filter(p=>(p.global||!!selectedCluster.value)&&allowed(p.id)).map(p=>({id:'page-'+p.id,label:p.title,kind:t('Раздел'),context:p.global?t('Платформа'):scope,run:()=>navigate(p.id)}));
 if(isAuthenticated.value){
  for(const c of clusters.value) all.unshift({id:'cluster-'+c.id,label:c.name,kind:t('Кластер'),context:t('Платформа'),run:()=>{switchCluster(c.id);navigate('overview');}});
  for(const n of nodes.value) all.push({id:'node-'+n.ip,label:n.hostname||n.ip,search:n.ip,kind:t('Нода'),context:scope,run:()=>{inspected.value=n;}});
  for(const p of pods.value) all.push({id:'pod-'+p.namespace+'/'+p.name,label:p.name,kind:'Pod',context:`${scope} / ${p.namespace}`,run:()=>openWorkload({namespace:p.namespace,name:p.name})});
  if(isAdmin.value) all.push({id:'import',label:t('Добавить кластер'),kind:t('Действие'),context:t('Платформа'),run:()=>{actionError.value='';dialog.value='import';}});
  if(selectedCluster.value&&isAdmin.value) all.push({id:'worker',label:t('Добавить worker'),kind:t('Действие'),context:scope,run:()=>{dialog.value='create-worker';}});
  if(selectedCluster.value&&canOperate.value) all.push({id:'upgrade',label:t('Обновить Talos'),kind:t('Действие'),context:scope,run:()=>{operationKind.value='talos-upgrade';navigate('updates');}});
 }
 return all;
});
const workloadFocus=ref<{namespace?:string;name?:string;node?:string}|null>(null);
function openWorkload(focus:{namespace?:string;name?:string;node?:string}) {workloadFocus.value=focus;inspected.value=null;navigate('workloads');}
function inspectRelatedNode(name:string, logs=false) {const node=nodes.value.find(n=>n.hostname===name||n.ip===name);if(!node){notify(t('Нода отсутствует в текущем списке. Обновите состояние кластера.'));return;}if(logs){nodeIP.value=node.ip;navigate('logs');}else inspected.value=node;}
function relatedKubernetes(mode:string, namespace:string) {workloadFocus.value={namespace:namespace==='—'?undefined:namespace};navigate(mode);}
function nodeStorage(node:NodeOverview) {nodeIP.value=node.ip;inspected.value=null;navigate('storage');}
const settingsScope=computed(()=>['platform-settings','providers','users'].includes(active.value)?'platform':['cluster-settings','settings','config','alerts'].includes(active.value)?'cluster':'');

const confirmation = ref<{
  title: string;
  description: string;
  run: () => Promise<unknown>;
} | null>(null);
let toastTimer: ReturnType<typeof setTimeout>;
let timer: ReturnType<typeof setTimeout> | null = null;
let disposed = false;
let generation = 0;
const checks = ref<any[]>([]);
const operationKind = ref<
  "talos-upgrade" | "kubernetes-upgrade" | "rolling-reboot"
>("talos-upgrade");
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
const issueRows = computed(() => [
  ...nodes.value
    .filter((n) => !n.ready)
    .map((n) => ({
      name: n.hostname,
      reason: t("Нода не готова"),
      kind: "nodes",
    })),
  ...nodes.value.flatMap((n) =>
    Object.entries(n.servicesSummary || {})
      .filter(([, v]) => v === "Degraded")
      .map(([service]) => ({
        name: `${n.hostname} / ${service}`,
        reason: t("Сервис деградирован"),
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
          reason: t("Проблема кворума или участников"),
          kind: "etcd",
        },
      ]
    : []),
]);
const nodeColumns = computed(() => [
  { key: "hostname", title: t("Имя"), mono: true },
  { key: "status", title: t("Состояние") },
  { key: "role", title: t("Роль") },
  { key: "ip", title: t("Адрес"), mono: true },
  { key: "cpu", title: "CPU", mono: true },
  { key: "memory", title: t("Память"), mono: true },
  { key: "version", title: "Talos", mono: true },
  { key: "uptime", title: t("Время работы"), mono: true },
]);
function notify(message: string) {
  toast.value = message;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toast.value = ""), 6000);
}
async function perform(
  run: () => Promise<unknown>,
  message = t("Операция выполнена"),
) {
  if (busy.value) return;
  const epoch = clusterEpoch();
  busy.value = true;
  actionError.value = "";
  try {
    await run();
    if (epoch !== clusterEpoch()) return false;
    notify(message);
    return true;
  } catch (e) {
    if (epoch === clusterEpoch())
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
  const epoch = clusterEpoch();
  try {
    const result = await request(path);
    if (!disposed && epoch === clusterEpoch()) {
      set(result);
      delete errors.value[key];
    }
  } catch (e) {
    if (!disposed && epoch === clusterEpoch())
      errors.value[key] = e instanceof Error ? e.message : String(e);
  }
}
async function refresh() {
  if (globalPage.value) {
    await loadClusters();
    return;
  }
  if (loading.value || !selectedCluster.value || !isAuthenticated.value) return;
  const epoch = clusterEpoch();
  loading.value = true;
  await Promise.allSettled([
    probe("nodes", "/nodes", (v) => {
      nodes.value = list(v);
      if (!nodes.value.some((n) => n.ip === nodeIP.value))
        nodeIP.value = nodes.value[0]?.ip || "";
    }),
    probe("cluster", "/cluster", (v) => (cluster.value = v)),
    probe("pods", "/k8s/pods", (v) => (pods.value = list(v))),
    probe("jobs", "/jobs", (v) => (recentJobs.value = list(v).map(job=>({...job,kind:job.request?.kind || job.kind})))),
    probe("backups", "/backups", (v) => (recentBackups.value = list(v))),
    probe("diagnostics", "/diagnostics", (v) => (diagnostics.value = v)),
    probe("health", "/health-score", (v)=>(healthReport.value=v)),
    probe("certificates", "/certificates", (v) => (certificates.value = v)),
    probe("notifications", "/notifications/status", (v) => (notificationStatus.value = v)),
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
  ]);
  if (!disposed && epoch === clusterEpoch()) {
    refreshed.value = new Date().toLocaleTimeString("ru-RU");
    loading.value = false;
  }
}
function schedule() {
  if (timer) clearTimeout(timer);
  if (interval.value && !disposed)
    timer = setTimeout(async () => {
      await refresh();
      if (["storage"].includes(active.value)) await loadSection();
      schedule();
    }, interval.value * 1000);
}
async function loadSection() {
  if (globalPage.value) return;
  if (!selectedCluster.value || !isAuthenticated.value) return;
  const id = ++generation;
  const section = active.value;
  if (["config", "alerts"].includes(section) && !isAdmin.value) return;
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
        throw new Error(t("Не удалось получить диски выбранной ноды"));
    }
    if (section === "config" && nodeIP.value) {
      const r = await request(
        `/nodes/${encodeURIComponent(nodeIP.value)}/config`,
      );
      if (id === generation) config.value = r.configYaml || r.yaml || "";
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
function switchCluster(id: string) {
  const same = selectedCluster.value === id;
  selectCluster(id);
  if (globalPage.value) navigate("overview");
  if (same) {
    refresh().then(loadSection);
  }
}
const hash = () => {
  active.value = initial();
};
watch(active, () => {
  expandedGroups.value = [page.value.group];
  if (!globalPage.value && !refreshed.value) refresh();
  loadSection();
});
watch(nodeIP, () => {
  if (["storage", "config"].includes(active.value)) loadSection();
});
watch(interval, schedule);
watch(selectedCluster, async () => {
  generation++;
  nodes.value = [];
  cluster.value = null;
  pods.value = [];
  recentJobs.value = [];
  recentBackups.value = [];
  diagnostics.value = null;
  certificates.value = null;
  healthReport.value=null;
  notificationStatus.value=null;
  workloadFocus.value=null; paletteOpen.value=false;
  etcd.value = null;
  nodeIP.value = "";
  data.value = [];
  config.value = "";
  detail.value = null;
  inspected.value = null;
  confirmation.value = null;
  dialog.value = "";
  checks.value = [];
  importForm.value = { name: "", talosconfig: "", kubeconfig: "" };
  role.value = "all";
  actionError.value = "";
  errors.value = {};
  refreshed.value = "";
  loading.value = false;
  sectionLoading.value = false;
  await refresh();
  await loadSection();
});
watch(isAuthenticated, (authorized) => {
  if (!authorized) {
    generation++;
    config.value = "";
    data.value = [];
      detail.value = null;
    inspected.value = null;
    sectionLoading.value = false;
    selectCluster("");
    clusters.value = [];
  }
});
onMounted(async () => {
  window.addEventListener("hashchange", hash);
  window.addEventListener('keydown',shortcut);
  const code = new URLSearchParams(location.hash.slice(1)).get("oidc_code");
  if (code) {
    history.replaceState(
      null,
      "",
      location.pathname + location.search + "#clusters",
    );
    try {
      const result = await post("/auth/oidc/exchange", { code });
      if (!result.token) throw new Error(t("Не удалось выполнить вход"));
      localStorage.setItem(TOKEN_STORAGE_KEY, result.token);
      isAuthenticated.value = true;
      currentUser.value = result.user;
    } catch (e) {
      registryError.value = String(e);
    }
  }
  void request('/auth/providers').then(result=>{if(!disposed)oidc.value=result?.oidc||null;}).catch(()=>{if(!disposed)oidc.value=null;});
  await getMe();
  if (!isAuthenticated.value) selectCluster("");
  await loadClusters();
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
  window.removeEventListener('keydown',shortcut);
});
async function signIn() {
  if (
    await perform(async () => {
      const r = await login(password.value, username.value);
      if (!r.success) throw new Error(r.error);
    }, t("Вход выполнен"))
  ) {
    dialog.value = "";
    password.value = "";
    await loadClusters();
    await refresh();
    await loadSection();
  }
}
function rollingReboot() {
  operationKind.value = "rolling-reboot";
  navigate("updates");
}
const protectedPage = computed(
  () =>
    !isAuthenticated.value ||
    (["config", "providers", "machines", "fleet-machines", "alerts"].includes(active.value) &&
      !isAdmin.value) ||
    (active.value === "updates" && !canOperate.value),
);
</script>

<template>
  <div class="console-app">
    <aside :class="['rail', { open: mobile }]">
      <a class="brand" href="#clusters" @click.prevent="navigate('clusters')"
        ><span class="brand-symbol">t<span>_</span></span>
        <div>TalosDeck<small>INFRASTRUCTURE CONSOLE</small></div></a
      ><button
        class="mobile-close icon-button"
        :aria-label="t('Закрыть навигацию')"
        @click="mobile = false"
      >
        <X :size="20" />
      </button>
      <div class="cluster-picker">
        <label
          >{{ t("Кластер")
          }}<select
            :value="selectedCluster"
            :disabled="busy || importing"
            :aria-label="t('Кластер')"
            @change="switchCluster(($event.target as HTMLSelectElement).value)"
          >
            <option v-if="!clusters.length" value="">
              {{ t("Нет кластеров") }}
            </option>
            <option v-for="item in clusters" :key="item.id" :value="item.id">
              {{ item.name }}
            </option>
          </select></label
        >
        <button
          v-if="isAdmin"
          @click="
            dialog = 'import';
            actionError = '';
          "
        >
          <Plus :size="14" />{{ t("Добавить кластер") }}
        </button>
      </div>
      <label class="nav-search"
        ><Search :size="15" /><input
          v-model="navSearch"
          :placeholder="t('Найти раздел')"
          :aria-label="t('Найти раздел')"
      /></label>
      <nav :aria-label="t('Главная навигация')">
        <template v-for="group in navGroups" :key="group"
          ><button
            v-if="selectedCluster || group === t('ПЛАТФОРМА')"
            class="nav-group group-toggle"
            :aria-expanded="
              !!navSearch ||
              expandedGroups.includes(group) ||
              group === page.group ||
              group === t('ПЛАТФОРМА')
            "
            @click="toggleGroup(group)"
          >
            {{ group }}<ChevronRight :size="12" />
          </button>
          <template
            v-if="
              navSearch ||
              expandedGroups.includes(group) ||
              group === page.group ||
              group === t('ПЛАТФОРМА')
            "
          >
            <button
              v-for="item in pages.filter(
                (p) =>
                  p.group === group &&
                  (p.global || !!selectedCluster) &&
                  (!p.hidden || !!navSearch) &&
                  (p.id !== 'providers' || isAdmin) &&
                  p.title.toLowerCase().includes(navSearch.toLowerCase()),
              )"
              :key="item.id"
              :class="{ active: active === item.id }"
              :aria-current="active === item.id ? 'page' : undefined"
              @click="navigate(item.id)"
            >
              <component :is="item.icon" :size="17" /><span>{{
                item.title
              }}</span
              ><span v-if="item.id === 'nodes'" class="nav-count">{{
                nodes.length
              }}</span>
            </button></template
          ></template
        >
      </nav>
      <div class="rail-footer">
        <label class="language-switch">
          <span>{{ t("Язык интерфейса") }}</span>
          <select
            :value="locale"
            :aria-label="t('Язык интерфейса')"
            @change="setLocale(($event.target as HTMLSelectElement).value)"
          >
            <option value="ru">Русский</option>
            <option value="en">English</option>
          </select>
        </label>
        <a
          href="https://github.com/etosheartem/TalosDeck"
          target="_blank"
          rel="noopener noreferrer"
        >
          {{ t("Проект на GitHub") }} <ArrowUpRight :size="13" />
        </a>
        <a href="https://www.talos.dev" target="_blank" rel="noopener">
          {{ t("Документация Talos") }} <ArrowUpRight :size="13"
        /></a>
      </div>
    </aside>
    <button
      v-if="mobile"
      class="nav-backdrop"
      :aria-label="t('Закрыть меню')"
      @click="mobile = false"
    />
    <div class="workspace">
      <header class="topline">
        <button
          class="mobile-menu icon-button"
          :aria-label="t('Открыть меню')"
          @click="mobile = true"
        >
          <Menu :size="20" />
        </button>
        <div class="breadcrumbs">
          <Network :size="16" /><button
            class="breadcrumb-link"
            @click="navigate('clusters')"
          >
            {{
              globalPage
                ? "TalosDeck"
                : clusters.find((c) => c.id === selectedCluster)?.name ||
                  cluster?.name ||
                  t("Кластер")
            }}</button
          ><ChevronRight :size="14" /><template v-if="settingsScope && !['platform-settings','cluster-settings'].includes(active)"><button @click="navigate(settingsScope==='platform'?'platform-settings':'cluster-settings')">{{ settingsScope==='platform'?t('Настройки платформы'):t('Настройки кластера') }}</button><ChevronRight :size="14" /></template><strong>{{ page.title }}</strong>
        </div>
        <div class="session">
          <span class="viewer-label">{{
            isAuthenticated ? currentUser.username : t("Только просмотр")
          }}</span
          ><button
            v-if="!isAuthenticated"
            @click="
              dialog = 'login';
              actionError = '';
            "
          >
            <LogIn :size="15" /> {{ t("Войти") }}</button
          ><button
            v-else
            @click="
              signOut()
            "
          >
            <LogOut :size="15" /> {{ t("Выйти") }}
          </button>
        </div>
      </header>
      <main>
        <div v-if="recoveryState === 'safe'" class="notice error recovery-banner" role="alert">
          <AlertTriangle :size="20" aria-hidden="true" />
          <div><strong>{{ t('Безопасный режим после восстановления') }}</strong><p>{{ t('TalosDeck восстановлен из резервной копии. Изменения инфраструктуры, фоновые задания и расписания заблокированы. Администратор должен проверить результаты незавершённых операций и отключение прежнего экземпляра перед возобновлением управления. Автоматического продолжения нет.') }}</p></div>
        </div>
        <p v-if="logoutWarning" class="notice error" role="alert">{{ logoutWarning }}</p>
        <div v-if="registryError" class="notice error" role="alert">
          {{ registryError
          }}<button @click="loadClusters">{{ t("Повторить") }}</button>
        </div>
        <div v-if="!isAuthenticated" class="access-state">
          <h1>{{ t("Кластеры") }}</h1>
          <p>
            {{
              t(
                "Подключите Talos и Kubernetes, чтобы начать управление инфраструктурой.",
              )
            }}
          </p>
          <button
            class="primary"
            @click="
              dialog = isAuthenticated ? 'import' : 'login';
              actionError = '';
            "
          >
            {{ isAuthenticated ? t("Добавить кластер") : t("Войти") }}
          </button>
        </div>
        <template v-else>
          <div class="page-heading">
            <div>
              <div class="eyebrow">{{ page.group }} / TALOS LINUX</div>
              <h1>{{ page.title }}</h1>
              <p>{{ page.description }}</p>
            </div>
            <div v-if="!globalPage && active!=='health'" class="refresh-tools">
              <span v-if="refreshed"> {{ t("Опрос") }} {{ refreshed }}</span
              ><select
                v-model="interval"
                :aria-label="t('Интервал обновления')"
              >
                <option :value="0">{{ t("Вручную") }}</option>
                <option :value="10">{{ t("10 сек") }}</option>
                <option :value="30">{{ t("30 сек") }}</option>
                <option :value="60">{{ t("1 мин") }}</option></select
              ><button
                :disabled="loading || sectionLoading"
                @click="refresh().then(loadSection)"
              >
                <RefreshCw :size="15" :class="{ spin: loading }" />
                {{ t("Обновить") }}
              </button>
            </div>
          </div>
          <nav v-if="settingsScope && !['platform-settings','cluster-settings'].includes(active)" class="section-tabs" :aria-label="t('Разделы настроек')"><button @click="navigate(settingsScope==='platform'?'platform-settings':'cluster-settings')">{{ t('Все настройки') }}</button><button v-for="item in pages.filter(p=>settingsScope==='platform'?['providers','users'].includes(p.id)&& (p.id!=='providers'||isAdmin):['settings','config','alerts'].includes(p.id)&&(p.id==='settings'||isAdmin))" :key="item.id" :aria-current="active===item.id?'page':undefined" @click="navigate(item.id)">{{ item.title }}</button></nav>
          <div
            v-if="
              !globalPage &&
              Object.keys(errors).filter((k) => k !== 'section').length
            "
            class="notice warning"
            role="status"
          >
            <AlertTriangle :size="18" />
            <div>
              <strong> {{ t("Часть данных недоступна") }} </strong>
              <p>
                {{ t("Последние полученные значения могут быть устаревшими.") }}
              </p>
              <details>
                <summary>{{ t("Подробности") }}</summary>
                <p v-for="(message, key) in errors" :key="key">
                  {{ key }}: {{ message }}
                </p>
              </details>
            </div>
          </div>
          <div
            v-if="!globalPage && loading && !refreshed"
            class="loading-state"
          >
            <RefreshCw :size="22" class="spin" />
            {{ t("Подключение к кластеру…") }}
          </div>
          <section v-if="active === 'clusters'" class="panel fleet-overview">
            <header>
              <h2>{{ t("Подключённые кластеры") }}</h2>
              <div class="toolbar">
                <button @click="loadClusters">{{ t("Обновить") }}</button
                ><button
                  v-if="isAdmin"
                  @click="
                    dialog = 'import';
                    actionError = '';
                  "
                >
                  {{ t("Добавить кластер") }}</button
                ><button
                  v-if="isAdmin"
                  class="primary"
                  @click="dialog = 'create-cluster'"
                >
                  {{ t("Создать кластер") }}
                </button><button v-if="isAdmin" @click="navigate('templates')">{{t('Из шаблона')}}</button>
              </div>
            </header>
            <ResourceTable
              :rows="clusters"
              :empty="t('Подключите существующий кластер или создайте новый')"
              :columns="[
                { key: 'name', title: t('Имя') },
                { key: 'health', title: t('Состояние') },
                { key: 'talosVersion', title: 'Talos' },
                { key: 'kubernetesVersion', title: 'Kubernetes' },
                { key: 'provider', title: t('Провайдер') },
                { key: 'endpoints', title: t('Адреса'), mono: true },
              ]"
              @select="switchCluster($event.id)"
            />
            <p class="footnote">
              {{
                t(
                  "Выберите кластер, чтобы открыть ноды, операции и диагностику.",
                )
              }}
            </p>
            <details v-if="isAdmin" class="fleet-jobs" :open="showGlobalJobs" @toggle="showGlobalJobs=($event.target as HTMLDetailsElement).open"><summary>{{t('Создание кластеров')}}</summary><JobsView mode="jobs" global /></details>
          </section>
          <ProvidersView v-else-if="active === 'providers' && isAdmin" />
          <SecurityView v-else-if="active === 'users'" />
          <div v-else-if="!selectedCluster" class="access-state">
            <h2>{{ t("Нет выбранного кластера") }}</h2>
            <button @click="navigate('clusters')">
              {{ t("Открыть кластеры") }}
            </button>
          </div>
          <template v-else-if="active === 'overview'">
            <div class="overview-grid">
              <section class="availability">
                <div class="section-label">{{ t("ДОСТУПНОСТЬ КЛАСТЕРА") }}</div>
                <div class="availability-value">
                  <span>{{ refreshed && !errors.nodes ? ready : "—" }}</span
                  ><small>/ {{ nodes.length || "—" }} {{ t("нод") }} </small>
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
                        ? t("Нет актуальных данных")
                        : nodes.length
                          ? ready === nodes.length
                            ? t("Все ноды готовы")
                            : t("Требует внимания")
                          : t("Ожидание данных")
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
                  {{ t("Открыть список нод") }} <ArrowUpRight :size="16" />
                </button>
              </section>
              <section class="overview-metrics">
                <div>
                  <span>Control plane</span
                  ><strong>{{
                    nodes.filter((n) => n.role === "controlplane").length
                  }}</strong
                  ><small> {{ t("Управление кластером") }} </small>
                </div>
                <div>
                  <span>Workers</span
                  ><strong>{{
                    nodes.filter((n) => n.role === "worker").length
                  }}</strong
                  ><small> {{ t("Вычислительные ноды") }} </small>
                </div>
                <div>
                  <span> {{ t("Поды") }} </span
                  ><strong>{{ errors.pods ? "—" : pods.length }}</strong
                  ><small
                    >{{ troubled.length }} {{ t("требуют внимания") }}
                  </small>
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
                  ><small>
                    {{ t("Участники:") }}
                    {{ etcd?.members?.length ?? "—" }}</small
                  >
                </div>
              </section>
            </div>
            <div class="two-columns">
              <section class="surface">
                <header class="surface-heading">
                  <h2>{{ t("Ресурсы машин") }}</h2>
                  <span> {{ t("Текущий срез") }} </span>
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
                  {{ t("Нет данных о машинах") }}
                </div>
              </section>
              <section class="surface">
                <header class="surface-heading">
                  <h2>{{ t("Требует внимания") }}</h2>
                  <span>{{ issueRows.length }}</span>
                </header>
                <div v-if="!issueRows.length" class="quiet-state">
                  <CheckCircle2 :size="28" /><strong>{{
                    (loading && !refreshed) || Object.keys(errors).length
                      ? t("Проверка неполная")
                      : t("Активных проблем не обнаружено")
                  }}</strong>
                  <p>
                    {{
                      (loading && !refreshed) || Object.keys(errors).length
                        ? t("Часть источников ещё не ответила.")
                        : t("По последним ответам Talos и Kubernetes.")
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
            <div class="dashboard-recent">
              <section class="surface"><header class="surface-heading"><h2>{{ t('Последние задания') }}</h2><button @click="navigate('jobs')">{{ t('Все') }}</button></header>
                <ResourceTable :rows="recentJobs.slice(0,5)" :search="false" :columns="[{key:'kind',title:t('Тип')},{key:'status',title:t('Состояние')}]" @select="navigate('jobs')" />
              </section>
              <section class="surface"><header class="surface-heading"><h2>{{ t('Последние копии') }}</h2><button @click="navigate('backups')">{{ t('Резервные копии') }}</button></header>
                <ResourceTable :rows="recentBackups.slice(0,3)" :search="false" :columns="[{key:'filename',title:t('Имя')},{key:'timestamp',title:t('Время')}]" @select="navigate('backups')" />
              </section>
              <section class="surface"><header class="surface-heading"><h2>{{ t('Последняя диагностика') }}</h2><button @click="navigate('diagnostics')">{{ t('Диагностика') }}</button></header>
                <p class="notice">{{ diagnostics?.status && diagnostics.status !== 'unknown' ? diagnostics.checkedAt : t('Диагностика ещё не выполнялась') }} · {{ diagnostics?.status || 'unknown' }}</p>
                <button v-for="check in (diagnostics?.checks || []).filter((c:any)=>['critical','warning'].includes(c.severity)).slice(0,5)" :key="check.id" class="issue-row" @click="navigate('diagnostics')"><AlertTriangle :size="16"/><span>{{ check.title }}<small>{{ check.node || check.component }}</small></span></button>
              </section>
            </div>
            <section class="surface"><header class="surface-heading"><h2>{{ t('Оповещения') }}</h2><button @click="navigate('alert-center')">{{ t('Центр оповещений') }}</button></header><p class="notice">{{ errors.notifications||!notificationStatus?.summary||!notificationStatus.lastCheckAt||notificationStatus.lastCheckAt.startsWith('0001-') ? t('Неизвестно'):notificationLabel(notificationStatus.health) }} · {{ t('Активно') }}: {{ notificationStatus?.summary?.active??'—' }} · {{ t('Критические') }}: {{ notificationStatus?.summary?.critical??'—' }} · {{ t('Устарело') }}: {{ notificationStatus?.summary?.stale??'—' }}</p><p class="footnote">{{ t('Последняя проверка') }}: {{ notificationTime(notificationStatus?.lastCheckAt) }}</p></section>
            <section class="surface"><header class="surface-heading"><h2>{{t('Здоровье кластера')}}</h2><button @click="navigate('health')">{{t('Проверки и оценка')}}</button></header><p class="notice">{{errors.health||healthExpired(healthReport)||healthReport?.score==null?t('Недостаточно данных'):healthNumber(healthReport.score)+' / 100'}} · {{t('Покрытие проверками')}}: {{healthNumber(healthReport?.coverage)}}% · {{healthReport?.checkedAt||'—'}}</p><p v-if="healthReport?.findings?.some(c=>c.state==='critical')" class="notice error">{{t('Критические проблемы обнаружены')}}: {{healthReport.findings.filter(c=>c.state==='critical').length}}</p></section>
            <section class="surface"><header class="surface-heading"><h2>{{ t('Сертификаты') }}</h2><button @click="navigate('certificates')">{{ t('Проверить сроки') }}</button></header><p class="notice">{{ errors.certificates || !certificates?.certificates?.length ? t('Неизвестно') : certificateStatus(certificates.status) }} · {{ t('Требует внимания') }}: {{ certificates?.summary ? certificates.summary.critical + certificates.summary.warning : '—' }} · {{ t('Неизвестно') }}: {{ certificates?.summary?.unknown ?? '—' }}</p></section>
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
                <span>{{ t("Провайдер") }}</span
                ><code>{{
                  clusters.find((c) => c.id === selectedCluster)?.provider ||
                  "—"
                }}</code>
              </div>
            </section>
          </template>
          <template v-else-if="active === 'nodes'"
            ><div class="toolbar">
              <label>
                {{ t("Роль") }}
                <select v-model="role">
                  <option value="all">{{ t("Все роли") }}</option>
                  <option value="controlplane">Control plane</option>
                  <option value="worker">Worker</option>
                </select></label
              ><span class="spacer" /><button
                class="primary"
                :disabled="!isAdmin"
                @click="dialog = 'create-worker'"
              >
                <Plus :size="16" /> {{ t("Добавить worker") }}
              </button>
            </div>
            <ResourceTable
              :rows="nodeRows"
              :columns="nodeColumns"
              @select="inspected = $event"
          /></template>
          <KubernetesView
            v-else-if="['workloads', 'events', 'kube-storage'].includes(active)"
            :key="selectedCluster + active"
            :focus="workloadFocus"
            @node="inspectRelatedNode($event)"
            @logs="inspectRelatedNode($event,true)"
            @related="relatedKubernetes"
            :mode="
              active === 'kube-storage'
                ? 'storage'
                : active === 'events'
                  ? 'events'
                  : 'workloads'
            "
          />
          <template v-else-if="active === 'etcd'"
            ><div class="cluster-facts surface">
              <div>
                <span> {{ t("Лидер") }} </span
                ><code>{{ etcd?.leaderName || "—" }}</code>
              </div>
              <div>
                <span> {{ t("Размер базы") }} </span
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
                { key: 'name', title: t('Участник') },
                { key: 'status', title: t('Состояние') },
                { key: 'role', title: t('Роль') },
                { key: 'dbSize', title: t('Размер БД') },
                { key: 'peerURLs', title: 'Peer URLs', mono: true },
              ]"
              @select="detail = $event"
          /></template>
          <div v-else-if="protectedPage" class="access-state">
            <LogIn :size="28" />
            <h2>
              {{
                isAuthenticated ? t("Недостаточно прав") : t("Требуется вход")
              }}
            </h2>
            <p>
              {{ t("Ваша роль не разрешает управление этим разделом.") }}
            </p>
            <button
              v-if="!isAuthenticated"
              class="primary"
              @click="
                dialog = 'login';
                actionError = '';
              "
            >
              {{ t("Войти") }}
            </button>
          </div>
          <JobsView
            v-else-if="active === 'updates' || active === 'jobs'"
            :key="selectedCluster + active"
            :focus="workloadFocus"
            @node="inspectRelatedNode($event)"
            @logs="inspectRelatedNode($event,true)"
            @related="relatedKubernetes"
            :mode="active"
            :initial-kind="operationKind"
            @submitted="navigate('jobs')"
          />
          <AuditView v-else-if="active === 'audit' || active === 'global-audit'" :key="active + (globalPage ? 'global' : selectedCluster)" :global="active === 'global-audit'" />
          <MachinesView
            v-else-if="active === 'machines' || active === 'fleet-machines'"
            :key="active + selectedCluster"
            :global="active === 'fleet-machines'"
            @add="dialog = 'create-worker'"
            @submitted="active === 'fleet-machines' ? (showGlobalJobs=true,navigate('clusters')) : navigate('jobs')"
          />
          <TemplatesView v-else-if="active==='templates'" @submitted="showGlobalJobs=true; navigate('clusters')" />
          <ImagePicker v-else-if="active==='images'" />
          <AlertCenter v-else-if="active==='alert-center'" :key="selectedCluster" @node="inspectRelatedNode($event)" @logs="inspectRelatedNode($event,true)" />
          <NotificationSettings v-else-if="active==='alerts'" :key="selectedCluster" />
          <SettingsHub v-else-if="['platform-settings','cluster-settings'].includes(active)" :global="active==='platform-settings'" @navigate="navigate" />
          <HealthView v-else-if="active==='health'" :key="selectedCluster" @node="inspectRelatedNode($event)" @logs="inspectRelatedNode($event,true)" @diagnostics="navigate('diagnostics')" />
          <CertificatesView v-else-if="active === 'certificates'" :key="selectedCluster" />
          <DiagnosticsView
            v-else-if="active === 'diagnostics'"
            :key="selectedCluster"
            :nodes="nodes"
            @health="navigate('health')"
            @inspect="inspected = $event"
            @logs="nodeIP = $event.ip; navigate('logs')"
            @submitted="navigate('jobs')"
          />
          <section v-else-if="active === 'logs'" class="panel">
            <header>
              <h2>{{ t("Логи Talos") }}</h2>
              <label
                >{{ t("Машина")
                }}<select v-model="nodeIP">
                  <option v-for="node in nodes" :key="node.ip" :value="node.ip">
                    {{ node.hostname }} · {{ node.ip }}
                  </option>
                </select></label
              >
            </header>
            <NodeInspector
              v-if="nodes.find((n) => n.ip === nodeIP)"
              :key="selectedCluster + nodeIP"
              :node="nodes.find((n) => n.ip === nodeIP)!"
              initial-tab="logs"
              @workloads="openWorkload({node:nodes.find(n=>n.ip===nodeIP)?.hostname || nodeIP})"
              @storage="nodeStorage(nodes.find(n=>n.ip===nodeIP)!)"
              @changed="refresh"
            />
            <p v-else class="footnote">{{ t("Нет доступных нод") }}</p>
          </section>
          <section v-else-if="active === 'settings'" class="panel">
            <header>
              <h2>{{ t("Подключение к кластеру") }}</h2>
            </header>
            <dl class="detail-grid settings-form">
              <dt>{{ t("Имя") }}</dt>
              <dd>
                {{ clusters.find((c) => c.id === selectedCluster)?.name }}
              </dd>
              <dt>ID</dt>
              <dd>{{ selectedCluster }}</dd>
              <dt>Talos endpoints</dt>
              <dd>
                {{
                  clusters
                    .find((c) => c.id === selectedCluster)
                    ?.endpoints?.join(", ") || "—"
                }}
              </dd>
              <dt>{{ t("Провайдер") }}</dt>
              <dd>
                {{
                  clusters.find((c) => c.id === selectedCluster)?.provider ||
                  "—"
                }}
              </dd>
            </dl>
            <div class="toolbar">
              <button v-if="isAdmin" @click="navigate('providers')">
                {{ t("Открыть провайдеров") }}</button
              ><button v-if="isAdmin" @click="navigate('alerts')">
                {{ t("Настроить уведомления") }}</button
              ><button @click="navigate('users')">
                {{ t("Моя учётная запись") }}
              </button>
            </div>
          </section>
          <template v-else>
            <div
              v-if="['storage', 'config', 'maintenance'].includes(active)"
              class="toolbar"
            >
              <label>
                {{ t("Машина") }}
                <select v-model="nodeIP">
                  <option v-if="!nodes.length" value="">
                    {{ t("Нет доступных нод") }}
                  </option>
                  <option v-for="n in nodes" :key="n.ip" :value="n.ip">
                    {{ n.hostname }} · {{ n.ip }}
                  </option>
                </select></label
              >
            </div>
            <div v-if="errors.section" class="notice error" role="alert">
              {{ errors.section
              }}<button @click="loadSection">{{ t("Повторить") }}</button>
            </div>
            <div v-if="sectionLoading" class="loading-state">
              <RefreshCw :size="20" class="spin" /> {{ t("Загрузка раздела…") }}
            </div>
            <template v-else-if="active === 'storage'"
              ><ResourceTable
                :rows="data"
                :columns="[
                  { key: 'name', title: t('Устройство'), mono: true },
                  { key: 'node', title: t('Нода') },
                  { key: 'model', title: t('Модель') },
                  { key: 'size', title: t('Размер') },
                  { key: 'type', title: t('Тип') },
                  { key: 'status', title: t('Здоровье') },
                  { key: 'bus', title: t('Шина') },
                ]"
                @select="detail = $event"
              />
              <p class="footnote">
                {{
                  t(
                    "Выберите диск, чтобы увидеть разделы, файловые системы и точки монтирования.",
                  )
                }}
              </p></template
            >
            <ConfigView
              v-else-if="active === 'config'"
              :key="selectedCluster + nodeIP"
              :node="nodeIP"
              :config="config"
              @submitted="navigate('jobs')"
            />
            <BackupsView
              v-else-if="active === 'backups'"
              :key="selectedCluster"
              :cluster-name="
                clusters.find((c) => c.id === selectedCluster)?.name ||
                cluster?.name ||
                ''
              "
              @submitted="navigate('jobs')"
            />
            <template v-else-if="active === 'maintenance'"
              ><section class="surface operation-list">
                <article>
                  <div>
                    <h2>{{ t("Проверка кластера") }}</h2>
                    <p>
                      {{
                        t(
                          "Проверить доступность API, системные компоненты и рабочие нагрузки.",
                        )
                      }}
                    </p>
                  </div>
                  <button
                    :disabled="busy"
                    @click="
                      perform(async () => {
                        checks = await runBootstrapCheck();
                      }, t('Проверка завершена'))
                    "
                  >
                    {{ t("Запустить проверку") }}
                  </button>
                </article>
                <article>
                  <div>
                    <h2>{{ t("Режим обслуживания") }}</h2>
                    <p>
                      {{ t("Управление режимом обслуживания выбранной ноды.") }}
                    </p>
                  </div>
                  <div class="toolbar">
                    <button
                      :disabled="!canOperate || !nodeIP || busy"
                      @click="
                        ask(
                          t('Включить обслуживание'),
                          t('Нода {0} будет переведена в режим обслуживания.', [
                            nodeIP,
                          ]),
                          () =>
                            post(`/nodes/${nodeIP}/maintenance`, {
                              enable: true,
                            }),
                        )
                      "
                    >
                      {{ t("Включить") }}</button
                    ><button
                      :disabled="!canOperate || !nodeIP || busy"
                      @click="
                        ask(
                          t('Выключить обслуживание'),
                          t('Выйти из режима обслуживания на {0}.', [nodeIP]),
                          () =>
                            post(`/nodes/${nodeIP}/maintenance`, {
                              enable: false,
                            }),
                        )
                      "
                    >
                      {{ t("Выключить") }}
                    </button>
                  </div>
                </article>
                <article>
                  <div>
                    <h2>{{ t("Перезагрузить ноду") }}</h2>
                    <p>
                      {{
                        t("Рабочие нагрузки на {0} будут прерваны.", [
                          nodeIP || t("выбранной ноде"),
                        ])
                      }}
                      {{ t('Подов на ноде по последнему опросу: {0}.', [impactedPods]) }}
                    </p>
                  </div>
                  <button
                    class="danger"
                    :disabled="!canOperate || !nodeIP || busy"
                    @click="
                      ask(
                        t('Перезагрузить ноду'),
                        t('Подтвердите перезагрузку {0}.', [nodeIP]),
                        () => rebootNode(nodeIP),
                      )
                    "
                  >
                    {{ t("Перезагрузить") }}
                  </button>
                </article>
                <article>
                  <div>
                    <h2>{{ t("Последовательная перезагрузка") }}</h2>
                    <p>
                      {{
                        t(
                          "Сначала workers, затем control plane. Ожидание готовности каждой ноды.",
                        )
                      }}
                    </p>
                  </div>
                  <button
                    class="danger"
                    :disabled="!canOperate || !nodes.length || busy"
                    @click="rollingReboot()"
                  >
                    {{ t("Перезагрузить все") }}
                  </button>
                </article>
              </section>
              <ResourceTable
                v-if="checks.length"
                :rows="checks"
                :columns="[
                  { key: 'title', title: t('Проверка') },
                  { key: 'status', title: t('Результат') },
                  { key: 'detail', title: t('Подробности') },
                ]"
                @select="detail = $event"
            /></template>
          </template>
          <div
            v-if="actionError && !dialog && !confirmation && !detail"
            class="notice error"
            role="alert"
          >
            {{ actionError }}
          </div>
        </template>
      </main>
      <footer class="workspace-footer">
        <span>TalosDeck</span
        ><span>Talos Linux / Kubernetes</span>
      </footer>
    </div>
    <div v-if="toast" class="toast" role="status">
      <CheckCircle2 :size="18" />{{ toast
      }}<button
        class="icon-button"
        :aria-label="t('Закрыть сообщение')"
        @click="toast = ''"
      >
        <X :size="16" />
      </button>
    </div>
    <Modal
      v-if="dialog === 'create-cluster' || dialog === 'create-worker'"
      :title="
        dialog === 'create-cluster'
          ? t('Создать кластер')
          : t('Добавить worker')
      "
      wide
      @close="dialog = ''"
      ><ProvisionView
        :kind="dialog === 'create-cluster' ? 'cluster-create' : 'worker-create'"
        :cluster-name="clusters.find((c) => c.id === selectedCluster)?.name"
        @submitted="
          () => {
            const global = dialog === 'create-cluster';
            if(global)showGlobalJobs=true;
            dialog = '';
            navigate(global ? 'clusters' : 'jobs');
            loadClusters();
          }
        "
    /></Modal>
    <Modal
      v-if="dialog === 'import'"
      :title="t('Добавить кластер')"
      wide
      @close="
        () => {
          if (!importing) {
            dialog = '';
            importForm = { name: '', talosconfig: '', kubeconfig: '' };
          }
        }
      "
    >
      <form class="settings-form" @submit.prevent="importCluster">
        <label
          >{{ t("Имя")
          }}<input
            v-model="importForm.name"
            required
            maxlength="100"
            autocomplete="off"
        /></label>
        <label
          >talosconfig<input type="file" :aria-label="t('Загрузить файл {0}', ['talosconfig'])" :disabled="importing" @change="readImportFile($event, 'talosconfig')" /><textarea
            v-model="importForm.talosconfig"
            :disabled="importing"
            aria-label="talosconfig"
            required
            rows="8"
            spellcheck="false"
            autocomplete="off"
          />
        </label>
        <label
          >kubeconfig<input type="file" :aria-label="t('Загрузить файл {0}', ['kubeconfig'])" :disabled="importing" @change="readImportFile($event, 'kubeconfig')" /><textarea
            v-model="importForm.kubeconfig"
            :disabled="importing"
            aria-label="kubeconfig"
            required
            rows="8"
            spellcheck="false"
            autocomplete="off"
          />
        </label>
        <p>
          {{
            t(
              "Сервер проверит оба API. Данные доступа хранятся в зашифрованном виде.",
            )
          }}
        </p>
        <p v-if="actionError" class="notice error" role="alert">
          {{ actionError }}
        </p>
        <button class="primary" :disabled="importing">
          {{ importing ? t("Проверка подключения…") : t("Подключить") }}
        </button>
      </form>
    </Modal>
    <Modal v-if="dialog === 'login'" :title="t('Вход')" @close="dialog = ''"
      ><form class="settings-form" @submit.prevent="signIn">
        <label
          >{{ t("Пользователь")
          }}<input v-model="username" autocomplete="username" required
        /></label>
        <a
          v-if="oidc?.enabled"
          class="button oidc-login"
          :href="oidc.loginUrl"
          >{{ t("Войти через {0}", [oidc.name || "SSO"]) }}</a
        >
        <label>
          {{ t("Пароль") }}
          <input
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
          {{ busy ? t("Вход…") : t("Войти") }}
        </button>
      </form></Modal
    >
    <Modal
      v-if="confirmation"
      :title="confirmation.title"
      @close="!busy && (confirmation = null)"
      ><p>{{ confirmation.description }}</p>
      <div v-if="actionError" class="notice error">{{ actionError }}</div>
      <div class="dialog-actions">
        <button :disabled="busy" @click="confirmation = null">
          {{ t("Отмена") }}</button
        ><button class="danger" :disabled="busy" @click="confirm">
          {{ busy ? t("Выполняется…") : t("Подтвердить") }}
        </button>
      </div></Modal
    >
    <CommandPalette v-if="paletteOpen" :commands="commands" :resources-unavailable="!!selectedCluster && (!refreshed || !!errors.nodes || !!errors.pods)" :scope="globalPage ? t('Платформа') : clusters.find(c=>c.id===selectedCluster)?.name || t('Кластер')" @close="paletteOpen=false" />
    <Modal
      v-if="inspected"
      :title="inspected.hostname"
      wide
      @close="inspected = null"
      ><NodeInspector
        :key="selectedCluster + inspected.ip"
        :node="inspected"
        @workloads="openWorkload({node:inspected.hostname || inspected.ip})"
        @storage="nodeStorage(inspected)"
        @changed="refresh"
    /></Modal>
    <Modal
      v-if="detail"
      :title="
        detail.name ||
        detail.filename ||
        detail.action ||
        detail.title ||
        t('Подробности')
      "
      wide
      @close="detail = null"
      ><template v-if="active === 'backups'"
        ><div class="toolbar">
          <button
            :disabled="busy"
            @click="perform(() => downloadBackup(detail), t('Файл скачан'))"
          >
            {{ t("Скачать") }}</button
          ><button
            class="danger"
            :disabled="busy"
            @click="
              ask(
                t('Удалить резервную копию'),
                t('Файл {0} будет удалён.', [detail.filename]),
                async () => {
                  await deleteBackup(detail.id);
                  detail = null;
                },
              )
            "
          >
            {{ t("Удалить") }}
          </button>
        </div></template
      ><ResourceTable
        v-if="detail.partitions"
        :search="false"
        :rows="detail.partitions"
        :columns="[
          { key: 'device', title: t('Раздел') },
          { key: 'label', title: t('Метка') },
          { key: 'filesystem', title: t('ФС') },
          { key: 'mountpoint', title: t('Точка монтирования') },
          { key: 'size', title: t('Размер') },
          { key: 'used', title: t('Занято') },
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

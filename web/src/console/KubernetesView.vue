<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from "vue";
import { request, list } from "./client";
import { t } from "./i18n";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
const props = defineProps<{ mode: "workloads" | "events" | "storage" }>();
const groups = ref<Record<string, any[]>>({}),
  tab = ref(
    props.mode === "workloads"
      ? "pods"
      : props.mode === "events"
        ? "events"
        : "persistentVolumeClaims",
  ),
  error = ref(""),
  busy = ref(false),
  namespace = ref("all"),
  selected = ref<any>(null),
  inspection = ref<any>(null),
  inspectBusy = ref(false),
  inspectError = ref("");
let live = true;
let generation = 0;
const tabs = computed(() => Object.keys(groups.value));
const tabLabels: Record<string, string> = {pods:'Pods',deployments:'Deployments',daemonsets:'DaemonSets',statefulsets:'StatefulSets',jobs:'Jobs',cronjobs:'CronJobs',events:'Events',persistentVolumeClaims:'PVC',persistentVolumes:'PV',storageClasses:'StorageClasses'};
const namespaces = computed(() =>
  [
    ...new Set(
      Object.values(groups.value)
        .flat()
        .map((r) => r.namespace || r.metadata?.namespace)
        .filter(Boolean),
    ),
  ].sort(),
);
const rows = computed(() =>
  (groups.value[tab.value] || [])
    .map((r) => ({
      ...r,
      name: r.name || r.metadata?.name,
      namespace: r.namespace || r.metadata?.namespace || "—",
      status: r.status || r.phase || r.type || "—",
      ready:
        (r.desired != null ? `${r.ready ?? 0}/${r.desired}` : r.ready) ??
        (r.readyReplicas != null
          ? `${r.readyReplicas}/${r.replicas ?? 0}`
          : "—"),
      node: r.nodeName || r.node || r.involvedObject?.name || "—",
    }))
    .filter(
      (r) => namespace.value === "all" || r.namespace === namespace.value,
    ),
);
async function load() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    let result: any;
    if (props.mode === "workloads") {
      const [pods, workloads] = await Promise.all([
        request("/k8s/pods"),
        request("/k8s/workloads"),
      ]);
      result = { pods: list(pods), ...workloads };
    } else result = await request(`/k8s/${props.mode}`);
    if (live)
      groups.value = Object.fromEntries(
        Object.entries(result).filter(([, value]) => Array.isArray(value)),
      ) as Record<string, any[]>;
  } catch (e) {
    if (live) error.value = String(e);
  } finally {
    if (live) busy.value = false;
  }
}
async function inspect(row: any) {
  const id = ++generation;
  selected.value = row;
  inspection.value = null;
  inspectError.value = "";
  if (tab.value !== "pods") return;
  inspectBusy.value = true;
  try {
    const value = await request(
      `/k8s/pods/${encodeURIComponent(row.namespace)}/${encodeURIComponent(row.name)}`,
    );
    if (live && id === generation) inspection.value = value;
  } catch (e) {
    if (live && id === generation) inspectError.value = String(e);
  } finally {
    if (live && id === generation) inspectBusy.value = false;
  }
}
onMounted(load);
onUnmounted(() => {
  live = false;
  generation++;
});
</script>
<template>
  <section class="panel">
    <header>
      <div class="section-tabs" role="tablist">
        <button
          v-for="kind in tabs"
          :key="kind"
          role="tab"
          :aria-selected="tab === kind"
          :class="{ selected: tab === kind }"
          @click="
            tab = kind;
            selected = null;
          "
        >
          {{ tabLabels[kind] || kind }} <small>{{ groups[kind]?.length }}</small>
        </button>
      </div>
      <button :disabled="busy" @click="load">{{ t("Обновить") }}</button>
    </header>
    <div class="toolbar">
      <label
        >Namespace<select v-model="namespace">
          <option value="all">{{ t("Все") }}</option>
          <option v-for="ns in namespaces" :key="ns">{{ ns }}</option>
        </select></label
      >
    </div>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <div v-if="busy && !tabs.length" class="loading-state">
      {{ t("Загрузка…") }}
    </div>
    <ResourceTable
      v-else
      :rows="rows"
      :columns="
        mode === 'events'
          ? [
              { key: 'reason', title: t('Причина') },
              { key: 'message', title: t('Сообщение') },
              { key: 'namespace', title: 'Namespace' },
              { key: 'type', title: t('Тип') },
            ]
          : [
              { key: 'name', title: t('Имя') },
              { key: 'namespace', title: 'Namespace' },
              { key: 'status', title: t('Состояние') },
              { key: 'ready', title: 'Ready' },
              { key: 'node', title: t('Нода') },
            ]
      "
      @select="inspect"
    />
    <Modal
      v-if="selected"
      :title="selected.name || selected.reason"
      wide
      @close="
        selected = null;
        generation++;
      "
      ><div class="summary-strip">
        <span>{{ selected.namespace }}</span
        ><span>{{ selected.status }}</span
        ><span>{{ selected.node }}</span>
      </div>
      <p v-if="inspectError" class="notice error">{{ inspectError }}</p>
      <div v-if="inspectBusy" class="loading-state">{{ t("Загрузка…") }}</div>
      <template v-else-if="inspection">
        <h3>{{ t('Контейнеры') }}</h3>
        <ResourceTable :rows="inspection.containers || []" :search="false" :columns="[{key:'name',title:t('Имя')},{key:'image',title:'Image'},{key:'state',title:t('Состояние')},{key:'restarts',title:t('Перезапуски')}]" />
        <p v-for="warning in inspection.warnings || []" :key="warning" class="notice">{{ warning }}</p>
        <h3>{{ t("События") }}</h3>
        <ResourceTable
          :rows="inspection.events || []"
          :search="false"
          :columns="[
            { key: 'reason', title: t('Причина') },
            { key: 'message', title: t('Сообщение') },
          ]"
        />
        <h3>{{ t("Тома") }}</h3>
        <pre class="config-code">{{
          JSON.stringify(inspection.volumes || [], null, 2)
        }}</pre>
        <h3>{{ t("Логи") }}</h3>
        <pre class="config-code">{{
          typeof inspection.logs === "string"
            ? inspection.logs
            : JSON.stringify(inspection.logs || {}, null, 2)
        }}</pre>
      </template>
      <pre v-else class="config-code">{{
        JSON.stringify(selected, null, 2)
      }}</pre>
    </Modal>
  </section>
</template>

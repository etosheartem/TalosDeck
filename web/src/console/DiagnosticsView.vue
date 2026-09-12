<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from "vue";
import { request, post, downloadAPI } from "./client";
import { t } from "./i18n";
import {healthExpired,healthNumber} from "./health";
import { canOperate } from "./permissions";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
import type { NodeOverview } from "../types";
const props = defineProps<{ nodes: NodeOverview[] }>();
const emit = defineEmits<{ health: []; submitted: []; inspect: [node: NodeOverview]; logs: [node: NodeOverview] }>();
const report = ref<any>(null),
  error = ref(""),
  busy = ref(false),
  severity = ref("all"),
  selected = ref<any>(null);
const selectedNode = computed(() => selected.value?.node ? props.nodes.find(n => n.ip === selected.value.node || n.hostname === selected.value.node) : undefined);
let live = true;
const rows = computed(() =>
  (report.value?.checks || []).filter(
    (c: any) =>
      severity.value === "all" ||
      String(c.severity).toLowerCase() === severity.value,
  ),
);
async function run(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await fn();
  } catch (e) {
    if (live) error.value = String(e);
  } finally {
    if (live) busy.value = false;
  }
}
async function load() {
  const data = await request("/diagnostics");
  if (live) report.value = data;
}
async function diagnose() {
  await run(async () => {
    await post("/diagnostics/run");
    if (live) emit("submitted");
  });
}
onMounted(() => run(load));
onUnmounted(() => {
  live = false;
});
</script>
<template>
  <section class="panel">
    <header>
      <div>
        <h2>{{ t("Здоровье кластера") }}</h2>
        <p>{{ report?.checkedAt || t("Проверки ещё не выполнялись") }}</p>
      </div>
      <div class="toolbar">
        <button :disabled="busy" @click="run(load)">{{ t("Обновить") }}</button
        ><button
          v-if="canOperate"
          :disabled="busy || !report?.checkedAt"
          @click="
            run(() =>
              downloadAPI('/diagnostics/bundle', 'support-bundle.tar.gz'),
            )
          "
        >
          {{ t("Support bundle") }}</button
        ><button
          v-if="canOperate"
          class="primary"
          :disabled="busy"
          @click="diagnose"
        >
          {{ t("Проверить кластер") }}
        </button>
      </div>
    </header>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <p v-if="report?.snapshotId || report?.health?.snapshotId" class="notice">{{t('Снимок')}}: {{report.snapshotId || report.health.snapshotId}} · {{t('Оценка')}}: {{healthExpired(report.health)||report.health?.score==null?t('Недостаточно данных'):healthNumber(report.health.score)}} · {{t('Покрытие проверками')}}: {{report.health?.coverage ?? '—'}}% <button @click="emit('health')">{{t('Проверки и оценка')}}</button></p>
    <div class="summary-strip">
      <span
        >{{ t("Состояние") }}
        <strong>{{ report?.status || "unknown" }}</strong></span
      ><span
        >{{ t("Критические") }}
        <strong>{{ report?.summary?.critical || 0 }}</strong></span
      ><span
        >{{ t("Предупреждения") }}
        <strong>{{ report?.summary?.warning || 0 }}</strong></span
      >
    </div>
    <div class="toolbar">
      <label
        >{{ t("Уровень")
        }}<select v-model="severity">
          <option value="all">{{ t("Все") }}</option>
          <option value="critical">Critical</option>
          <option value="warning">Warning</option>
          <option value="info">Info</option>
        </select></label
      >
    </div>
    <div v-if="busy && !report" class="loading-state">{{ t("Загрузка…") }}</div>
    <ResourceTable
      v-else
      :rows="rows"
      :columns="[
        { key: 'title', title: t('Проверка') },
        { key: 'severity', title: t('Уровень') },
        { key: 'component', title: t('Компонент') },
        { key: 'node', title: t('Нода') },
      ]"
      @select="selected = $event"
    /><Modal v-if="selected" :title="selected.title" @close="selected = null"
      ><p class="inspection-copy">{{ selected.details }}</p>
      <h3>{{ t("Рекомендация") }}</h3>
      <p class="inspection-copy">{{ selected.suggestion || "—" }}</p>
      <div v-if="selectedNode" class="toolbar">
        <button @click="emit('inspect', selectedNode!); selected = null">{{ t("Открыть ноду") }}</button>
        <button @click="emit('logs', selectedNode!); selected = null">{{ t("Логи ноды") }}</button>
      </div>
      <p v-else-if="selected.node" class="footnote">{{ t("Нода отсутствует в текущем списке. Обновите состояние кластера.") }}</p>
      </Modal
    >
  </section>
</template>

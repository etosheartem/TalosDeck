<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { RefreshCw, Play, Square, Download } from "lucide-vue-next";
import { request as apiRequest, download } from "./client";
import { canOperate } from "./permissions";
import { t, locale } from "./i18n";
import Modal from "./Modal.vue";
import ResourceTable from "./ResourceTable.vue";

type Kind = "talos-upgrade" | "kubernetes-upgrade" | "rolling-reboot";
interface Operation {
  kind: Kind;
  version?: string;
  allowDowntime: boolean;
}
interface Plan {
  kind: Kind;
  version?: string;
  nodes: {
    ip: string;
    name: string;
    role: string;
    current: string;
    image?: string;
    skip: boolean;
  }[];
  warnings: string[];
  kubernetesVersion: string;
}
interface Job {
  id: string;
  clusterId?: string;
  request: Omit<Operation, "kind"> & { kind: string };
  user: string;
  status: string;
  createdAt: string;
  updatedAt: string;
  step: string;
  error?: string;
  stopRequested: boolean;
  reviewed: boolean;
  events?: { time: string; step: string; message: string }[];
}
const props = defineProps<{
  mode: "updates" | "jobs";
  initialKind?: Kind;
  global?: boolean;
}>();
const request = <T = any,>(path: string, init: RequestInit = {}) =>
  apiRequest<T>(
    props.global ? path.replace(/^\/jobs/, "/provision/jobs") : path,
    init,
    props.global ? "global" : "cluster",
  );
const post = (path: string, body: any = {}) =>
  request(path, { method: "POST", body: JSON.stringify(body) });
const emit = defineEmits<{ submitted: [id: string] }>();
const kind = ref<Kind>(props.initialKind || "talos-upgrade");
const version = ref("");
const allowDowntime = ref(false);
const plan = ref<{ plan: Plan; cluster: string } | null>(null);
const confirmedCluster = ref("");
const confirming = ref(false);
const reviewing = ref(false);
const busy = ref(false);
const error = ref("");
const pollError = ref("");
const jobList = ref<Job[]>([]);
const selected = ref<Job | null>(null);
const selectedID = ref("");
const logFilter = ref("");
let disposed = false;
let polling = false;
let selectionGeneration = 0;
let timer: ReturnType<typeof setTimeout>;
const operation = computed<Operation>(() => ({
  kind: kind.value,
  version: kind.value === "rolling-reboot" ? undefined : version.value.trim(),
  allowDowntime: allowDowntime.value,
}));
const valid = computed(
  () =>
    kind.value === "rolling-reboot" ||
    /^v?\d+\.\d+\.\d+$/.test(version.value.trim()),
);
const locked = computed(() =>
  jobList.value.some(
    (j) =>
      ["queued", "running"].includes(j.status) ||
      (j.status === "interrupted" && !j.reviewed),
  ),
);
const title = (value: string) =>
  ({
    "talos-upgrade": t("Обновление Talos"),
    "kubernetes-upgrade": t("Обновление Kubernetes"),
    "rolling-reboot": t("Последовательная перезагрузка"),
    "config-apply": t("Изменить конфигурацию"),
    "config-restore": t("Восстановить конфигурацию"),
    "cluster-create": t('Создать кластер'),
    "worker-create": t('Добавить worker'),
    "worker-delete": t('Удалить машину'),
    "machine-cleanup": t('Очистить ресурсы'),
    "backup-create": t('Создать копию'),
    "backup-restore": t('Восстановить резервную копию'),
    "diagnostics": t('Диагностика'),
  })[value] || value;
async function exportLog() {
  if (!selected.value || busy.value) return;
  const id = selected.value.id;
  busy.value = true;
  error.value = "";
  try {
    const job = await request(`/jobs/${encodeURIComponent(id)}/export`);
    if (!disposed) download(JSON.stringify(job, null, 2), `job-${id}.json`);
  } catch (e) {
    if (!disposed) error.value = String(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
const state = (value: string) =>
  ({
    queued: t("В очереди"),
    running: t("Выполняется"),
    succeeded: t("Завершено"),
    failed: t("Ошибка"),
    interrupted: t("Прервано"),
    stopped: t("Остановлено"),
    'verify-ready':t('Проверка готовности нод'),
    'wait-kubernetes':t('Ожидание Kubernetes'),
    'wait-talos':t('Ожидание Talos API'),
    'create-vm':t('Создание машины'),
    'boot-vm':t('Загрузка машины'),
    'discover-address':t('Определение адреса'),
    'generate-config':t('Подготовка конфигурации'),
    'apply-config':t('Применение конфигурации'),
    'bootstrap-etcd':t('Инициализация etcd'),
    'provision-preflight':t('Проверка перед созданием'),
    'import-cluster':t('Подключение кластера'),
  })[value] || value;
const date = (value: string) =>
  new Date(value).toLocaleString(locale.value === "ru" ? "ru-RU" : "en-US");
const rows = computed(() =>
  jobList.value.map((j) => ({
    ...j,
    name: `${title(j.request.kind)} · ${j.id.slice(0, 8)}`,
    target: j.request.version || "—",
    statusLabel: state(j.status),
    created: date(j.createdAt),
  })),
);
const logText = computed(() =>
  (selected.value?.events || [])
    .map((e) => `${date(e.time)} [${e.step}] ${e.message}`)
    .filter((line) =>
      line.toLowerCase().includes(logFilter.value.toLowerCase()),
    )
    .join("\n"),
);
watch(operation, () => {
  plan.value = null;
  confirming.value = false;
  confirmedCluster.value = "";
  error.value = "";
});
watch(
  () => props.initialKind,
  (value) => {
    if (value) kind.value = value;
  },
);
async function select(id: string) {
  const generation = ++selectionGeneration;
  selectedID.value = id;
  try {
    const job = await request<Job>(`/jobs/${encodeURIComponent(id)}`);
    if (!disposed && generation === selectionGeneration) selected.value = job;
  } catch (e) {
    if (!disposed && generation === selectionGeneration)
      pollError.value = String(e);
  }
}
async function load() {
  if (polling || disposed) return;
  polling = true;
  try {
    const data = await request<Job[]>("/jobs");
    if (disposed) return;
    jobList.value = data;
    pollError.value = "";
    const id = data.some((j) => j.id === selectedID.value)
      ? selectedID.value
      : data[0]?.id;
    if (id) await select(id);
    else selected.value = null;
  } catch (e) {
    if (!disposed) pollError.value = String(e);
  } finally {
    polling = false;
  }
}
async function poll() {
  await load();
  if (!disposed) timer = setTimeout(poll, 3000);
}
onMounted(poll);
onUnmounted(() => {
  disposed = true;
  selectionGeneration++;
  clearTimeout(timer);
});
async function preview() {
  busy.value = true;
  error.value = "";
  plan.value = null;
  try {
    const result = await post("/jobs/plan", operation.value);
    if (!disposed) plan.value = result;
  } catch (e) {
    if (!disposed) error.value = String(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function submit() {
  if (
    !plan.value ||
    confirmedCluster.value !== plan.value.cluster ||
    busy.value
  )
    return;
  busy.value = true;
  error.value = "";
  try {
    const job = (await post("/jobs", {
      ...operation.value,
      confirmedCluster: confirmedCluster.value,
    })) as Job;
    if (!disposed) {
      confirming.value = false;
      plan.value = null;
      selectedID.value = job.id;
      await load();
      emit("submitted", job.id);
    }
  } catch (e) {
    if (!disposed) error.value = String(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function act(action: "stop" | "acknowledge") {
  if (!selected.value || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await post(
      `/jobs/${selected.value.id}/${action}`,
      action === "acknowledge" ? { reviewed: true } : {},
    );
    reviewing.value = false;
    await load();
  } catch (e) {
    if (!disposed) error.value = String(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
</script>
<template>
  <div class="jobs-view">
    <div v-if="error" class="notice error" role="alert">{{ error }}</div>
    <div v-if="pollError" class="notice error" role="alert">
      {{ t("Журнал временно недоступен. Статусы могут быть устаревшими.") }}
      {{ pollError }}
    </div>
    <div v-if="locked && mode === 'updates'" class="notice warning">
      {{
        t("Другая операция выполняется или ожидает проверки после прерывания.")
      }}
    </div>
    <p v-else-if="locked" class="footnote">{{ t('Активных заданий: {0}', [jobList.filter(j=>['queued','running'].includes(j.status)).length]) }}<span v-if="jobList.some(j=>j.status==='interrupted'&&!j.reviewed)"> · {{ t('Прерванное задание требует проверки') }}</span></p>
    <template v-if="mode === 'updates' && canOperate">
      <section class="panel operation-form">
        <header>
          <h2>{{ t("Новая операция") }}</h2>
        </header>
        <form class="job-form" @submit.prevent="preview">
          <label
            >{{ t("Операция")
            }}<select v-model="kind" :disabled="busy">
              <option value="talos-upgrade">{{ t("Обновление Talos") }}</option>
              <option value="kubernetes-upgrade">
                {{ t("Обновление Kubernetes") }}
              </option>
              <option value="rolling-reboot">
                {{ t("Последовательная перезагрузка") }}
              </option>
            </select></label
          >
          <label v-if="kind !== 'rolling-reboot'"
            >{{ t("Целевая версия")
            }}<input
              v-model="version"
              :disabled="busy"
              placeholder="1.x.y"
              required
              pattern="v?[0-9]+\.[0-9]+\.[0-9]+"
              autocomplete="off"
          /></label>
          <label v-if="kind !== 'kubernetes-upgrade'" class="job-check"
            ><input
              v-model="allowDowntime"
              type="checkbox"
              :disabled="busy"
            />{{
              t("Допускаю простой API при единственной control plane ноде")
            }}</label
          >
          <p>
            {{
              t(
                "Версия задаётся явно. Понижение и пропуск minor-версий запрещены.",
              )
            }}
          </p>
          <button
            class="primary"
            :disabled="!valid || busy || locked || !!pollError"
          >
            <RefreshCw :size="15" :class="{ spin: busy }" />{{
              busy ? t("Проверка…") : t("Проверить план")
            }}
          </button>
        </form>
      </section>
      <section v-if="plan" class="panel operation-plan">
        <header>
          <h2>{{ t("План операции") }}</h2>
          <code>{{ plan.cluster }}</code>
        </header>
        <p>{{ t("Перед изменениями будет создан и проверен снимок etcd.") }}</p>
        <p>
          {{
            t(
              "TalosDeck на обновляемой ноде может быть остановлен drain-операцией. Для непрерывного выполнения запустите панель вне управляемого кластера.",
            )
          }}
        </p>
        <ResourceTable
          :rows="
            plan.plan.nodes.map((n, i) => ({
              ...n,
              order: i + 1,
              result: n.skip ? t('Уже обновлено') : t('Будет обновлено'),
            }))
          "
          :search="false"
          :columns="[
            { key: 'order', title: t('Порядок') },
            { key: 'name', title: t('Нода') },
            { key: 'role', title: t('Роль') },
            { key: 'current', title: t('Текущая версия') },
            { key: 'image', title: 'Installer' },
            { key: 'result', title: t('Действие') },
          ]"
        />
        <details>
          <summary>{{ t("Предупреждения проверки") }}</summary>
          <ul>
            <li v-for="warning in plan.plan.warnings" :key="warning">
              {{ warning }}
            </li>
          </ul>
        </details>
        <button
          class="primary"
          :disabled="locked || busy || !!pollError"
          @click="confirming = true"
        >
          <Play :size="15" />{{ t("Запустить задание") }}
        </button>
      </section>
    </template>
    <section class="panel">
      <header>
        <h2>{{ props.global?t('Создание кластеров'):t("Журнал заданий") }}</h2>
        <button @click="load">
          <RefreshCw :size="15" />{{ t("Обновить") }}
        </button>
      </header>
      <ResourceTable
        :rows="rows"
        :columns="[
          { key: 'name', title: t('Операция') },
          ...(props.global?[]:[{ key: 'target', title: t('Версия') }]),
          { key: 'statusLabel', title: t('Состояние') },
          { key: 'user', title: t('Пользователь') },
          { key: 'created', title: t('Создано') },
        ]"
        @select="select($event.id)"
      />
    </section>
    <section v-if="selected" class="panel execution-log">
      <header>
        <h2>{{ title(selected.request.kind) }}</h2>
        <span
          class="state"
          :class="
            selected.status === 'succeeded'
              ? 'good'
              : ['failed', 'interrupted'].includes(selected.status)
                ? 'bad'
                : 'muted'
          "
          >{{ state(selected.status) }}</span
        >
      </header>
      <div class="toolbar">
        <code>{{ selected.id }}</code
        ><span>{{ t("Шаг") }}: {{ state(selected.step) || "—" }}</span
        ><span class="spacer" />
        <button
          v-if="canOperate && ['running', 'queued'].includes(selected.status)"
          :disabled="busy || selected.stopRequested"
          @click="act('stop')"
        >
          <Square :size="14" />{{
            selected.stopRequested
              ? t("Остановка запрошена")
              : t("Остановить после шага")
          }}
        </button>
        <button
          v-if="
            canOperate &&
            selected.status === 'interrupted' &&
            !selected.reviewed
          "
          :disabled="busy"
          @click="reviewing = true"
        >
          {{ t("Проверить прерывание") }}
        </button>
        <button :disabled="busy" @click="exportLog">
          <Download :size="15" />{{ t("Экспорт") }}
        </button>
      </div>
      <p v-if="selected.error" class="notice error">{{ selected.error }}</p>
      <p v-if="selected.stopRequested">
        {{
          t(
            "Текущий шаг завершается. Остановка не отменяет уже отправленные команды.",
          )
        }}
      </p>
      <input
        v-model="logFilter"
        :placeholder="t('Фильтр журнала')"
        :aria-label="t('Фильтр журнала')"
      />
      <pre class="job-log" tabindex="0" :aria-label="t('Журнал выполнения')">{{
        logText || t("Ожидание сообщений…")
      }}</pre>
    </section>
    <Modal
      v-if="confirming && plan"
      :title="t('Подтвердить операцию')"
      @close="!busy && (confirming = false)"
    >
      <p>{{ title(kind) }} → {{ plan.plan.version || "—" }}</p>
      <p>
        {{
          t(
            "Проверки будут повторены на сервере. Задание продолжится после закрытия вкладки.",
          )
        }}
      </p>
      <label class="job-confirm"
        >{{ t("Введите имя кластера") }}<code>{{ plan.cluster }}</code
        ><input v-model="confirmedCluster" :disabled="busy" autocomplete="off"
      /></label>
      <p v-if="error" class="notice error">{{ error }}</p>
      <div class="dialog-actions">
        <button :disabled="busy" @click="confirming = false">
          {{ t("Отмена") }}</button
        ><button
          class="danger"
          :disabled="busy || confirmedCluster !== plan.cluster || locked"
          @click="submit"
        >
          {{ t("Подтвердить запуск") }}
        </button>
      </div>
    </Modal>
    <Modal
      v-if="reviewing"
      :title="t('Проверка прерванного задания')"
      @close="!busy && (reviewing = false)"
    >
      <p>
        {{
          t(
            "Команда могла выполниться до остановки сервера. Проверьте версии, здоровье и состояние обслуживания нод. Подтверждение снимет блокировку, но не повторит команду.",
          )
        }}
      </p>
      <p v-if="error" class="notice error">{{ error }}</p>
      <div class="dialog-actions">
        <button :disabled="busy" @click="reviewing = false">
          {{ t("Отмена") }}</button
        ><button class="danger" :disabled="busy" @click="act('acknowledge')">
          {{ t("Состояние кластера проверено") }}
        </button>
      </div>
    </Modal>
  </div>
</template>

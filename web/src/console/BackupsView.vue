<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { request, post, downloadAPI, bytes } from "./client";
import { t } from "./i18n";
import { isAdmin, canOperate } from "./permissions";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
const props = defineProps<{ clusterName: string }>();
const emit = defineEmits<{ submitted: [] }>();
const rows = ref<any[]>([]),
  targets = ref<any[]>([]),
  schedule = ref({
    enabled: false,
    intervalHours: 6,
    retention: 30,
    targetId: "local",
  }),
  tab = ref("copies"),
  busy = ref(false),
  error = ref(""),
  notice = ref(""),
  selected = ref<any>(null),
  plan = ref<any>(null),
  confirmation = ref(""),
  targetDialog = ref(false),
  backupType = ref("etcd"),
  targetID = ref("local");
const blankTarget = () => ({
  id: "",
  name: "",
  type: "s3",
  endpoint: "",
  bucket: "",
  region: "us-east-1",
  prefix: "talosdeck/",
  accessKey: "",
  secretKey: "",
});
const target = ref(blankTarget());
let live = true;
async function run(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await fn();
  } catch (e) {
    if (live) error.value = String(e);
  } finally {
    if (live) busy.value = false;
  }
}
async function load() {
  const [copies, destinations, policy] = await Promise.all([
    request("/backups"),
    canOperate.value ? request("/backups/targets") : Promise.resolve({targets:[]}),
    canOperate.value ? request("/backups/schedule") : Promise.resolve({}),
  ]);
  if (live) {
    rows.value = Array.isArray(copies) ? copies : copies.backups || [];
    targets.value = destinations.targets || [];
    schedule.value = { ...schedule.value, ...policy };
  }
}
async function create() {
  await run(async () => {
    await post("/backups/create", {
      type: backupType.value,
      targetId: targetID.value,
    });
    if (live) emit("submitted");
  });
}
async function saveSchedule() {
  await run(async () => {
    await request("/backups/schedule", {
      method: "PUT",
      body: JSON.stringify(schedule.value),
    });
    if (live) notice.value = t("Расписание сохранено");
  });
}
async function saveTarget() {
  await run(async () => {
    const value = { ...target.value };
    if (!value.id) value.id = crypto.randomUUID();
    await request("/backups/targets", {
      method: "PUT",
      body: JSON.stringify(value),
    });
    if (!live) return;
    targetDialog.value = false;
    target.value = blankTarget();
    await load();
  });
}
async function previewRestore() {
  await run(async () => {
    const value = await post("/backups/restore-plan", {
      backupId: selected.value.id,
    });
    if (live) {
      plan.value = value;
      confirmation.value = "";
    }
  });
}
async function restore() {
  if (confirmation.value !== props.clusterName || !plan.value) return;
  await run(async () => {
    await post("/backups/restore", {
      planId: plan.value.id,
      confirmedCluster: confirmation.value,
    });
    if (live) emit("submitted");
  });
}
async function remove() {
  if (confirmation.value !== selected.value.filename) return;
  await run(async () => {
    await request(`/backups/${encodeURIComponent(selected.value.id)}`, {
      method: "DELETE",
    });
    if (live) {
      selected.value = null;
      await load();
    }
  });
}
onMounted(() => run(load));
onUnmounted(() => {
  live = false;
  target.value = blankTarget();
});
</script>
<template>
  <section class="panel">
    <header>
      <div class="section-tabs" role="tablist">
        <button
          v-for="[value, title] in [
            ['copies', t('Резервные копии')],
            ['schedule', t('Расписание')],
            ['targets', t('Места хранения')],
          ].filter(([value])=>value==='copies'||canOperate)"
          :key="value"
          role="tab"
          :aria-selected="tab === value"
          @click="tab = value!"
        >
          {{ title }}
        </button>
      </div>
      <button :disabled="busy" @click="run(load)">{{ t("Обновить") }}</button>
    </header>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <template v-if="tab === 'copies'"
      ><div v-if="canOperate" class="toolbar">
        <label
          >{{ t("Тип")
          }}<select v-model="backupType">
            <option value="etcd">etcd</option>
            <option value="full">{{ t("Полная копия") }}</option>
          </select></label
        ><label
          >{{ t("Место хранения")
          }}<select v-model="targetID">
            <option value="local">Local</option>
            <option
              v-for="item in targets.filter((t) => t.id !== 'local')"
              :key="item.id"
              :value="item.id"
            >
              {{ item.name }}
            </option>
          </select></label
        ><button class="primary" :disabled="busy" @click="create">
          {{ t("Создать копию") }}
        </button>
      </div>
      <ResourceTable
        :rows="
          rows.map((r) => ({ ...r, sizeLabel: r.humanSize || bytes(r.size) }))
        "
        :columns="[
          { key: 'filename', title: t('Файл'), mono: true },
          { key: 'type', title: t('Тип') },
          { key: 'sizeLabel', title: t('Размер') },
          { key: 'timestamp', title: t('Создано') },
          { key: 'node', title: t('Нода') },
        ]"
        @select="
          selected = $event;
          plan = null;
          confirmation = '';
        "
    /></template>
    <form
      v-else-if="tab === 'schedule'"
      class="settings-form"
      @submit.prevent="saveSchedule"
    >
      <fieldset :disabled="!isAdmin || busy">
        <label class="check-label"
          ><input v-model="schedule.enabled" type="checkbox" />{{
            t("Автоматическое резервное копирование")
          }}</label
        >
        <div class="form-grid">
          <label
            >{{ t("Интервал, часы")
            }}<input
              v-model.number="schedule.intervalHours"
              type="number"
              min="1"
              required /></label
          ><label
            >{{ t("Хранить копий")
            }}<input
              v-model.number="schedule.retention"
              type="number"
              min="1"
              required /></label
          ><label
            >{{ t("Место хранения")
            }}<select v-model="schedule.targetId">
              <option value="local">Local</option>
              <option
                v-for="item in targets.filter((t) => t.id !== 'local')"
                :key="item.id"
                :value="item.id"
              >
                {{ item.name }}
              </option>
            </select></label
          >
        </div>
        <button v-if="isAdmin" class="primary">{{ t("Сохранить") }}</button>
      </fieldset>
    </form>
    <template v-else
      ><div class="toolbar">
        <button
          v-if="isAdmin"
          class="primary"
          @click="
            target = blankTarget();
            targetDialog = true;
          "
        >
          {{ t("Добавить S3 / MinIO") }}
        </button>
      </div>
      <ResourceTable
        :rows="targets"
        :columns="[
          { key: 'name', title: t('Имя') },
          { key: 'type', title: t('Тип') },
          { key: 'endpoint', title: t('Адрес') },
          { key: 'bucket', title: 'Bucket' },
          { key: 'configured', title: t('Настроен') },
        ]"
        @select="
          (item) => {
            if (isAdmin && item.type === 's3') {
              target = {
                ...blankTarget(),
                ...item,
                accessKey: '',
                secretKey: '',
              };
              targetDialog = true;
            }
          }
        "
    /></template>
    <Modal
      v-if="targetDialog"
      :title="t('Место хранения')"
      wide
      @close="
        () => {
          if (!busy) {
            targetDialog = false;
            target = blankTarget();
          }
        }
      "
      ><form class="settings-form" @submit.prevent="saveTarget">
        <div class="form-grid">
          <label>{{ t("Имя") }}<input v-model="target.name" required /></label
          ><label
            >Endpoint<input
              v-model="target.endpoint"
              type="url"
              placeholder="https://s3.example"
              required /></label
          ><label>Bucket<input v-model="target.bucket" required /></label
          ><label>Region<input v-model="target.region" required /></label
          ><label>Prefix<input v-model="target.prefix" /></label
          ><label
            >Access key<input
              v-model="target.accessKey"
              autocomplete="off"
              :placeholder="
                t('Оставьте пустым, чтобы сохранить текущий')
              " /></label
          ><label
            >Secret key<input
              v-model="target.secretKey"
              type="password"
              autocomplete="new-password"
              :placeholder="t('Оставьте пустым, чтобы сохранить текущий')"
          /></label>
        </div>
        <p v-if="error" class="notice error">{{ error }}</p>
        <button class="primary" :disabled="busy">{{ t("Сохранить") }}</button>
      </form></Modal
    >
    <Modal
      v-if="selected"
      :title="selected.filename"
      wide
      @close="!busy && (selected = null)"
      ><dl class="detail-grid">
        <dt>{{ t("Тип") }}</dt>
        <dd>{{ selected.type }}</dd>
        <dt>{{ t("Размер") }}</dt>
        <dd>{{ selected.humanSize || bytes(selected.size) }}</dd>
        <dt>SHA256</dt>
        <dd class="inspection-copy">{{ selected.checksum }}</dd>
      </dl>
      <div v-if="isAdmin" class="toolbar">
        <button
          :disabled="busy"
          @click="
            run(() =>
              downloadAPI(
                `/backups/${encodeURIComponent(selected.id)}/download`,
                selected.filename,
              ),
            )
          "
        >
          {{ t("Скачать") }}</button
        ><button :disabled="busy" @click="previewRestore">
          {{ t("Проверить восстановление") }}
        </button>
      </div>
      <template v-if="plan"
        ><h3>{{ t("План восстановления") }}</h3>
        <p
          v-for="warning in plan.warnings || []"
          :key="warning"
          class="notice warning"
        >
          {{ warning }}
        </p>
        <ol>
          <li v-for="step in plan.steps || []" :key="step">{{ step }}</li>
        </ol>
        <p class="inspection-copy">{{ (plan.nodes || []).join(", ") }}</p>
        <form class="settings-form" @submit.prevent="restore">
          <label
            >{{ t("Введите имя кластера") }}<code>{{ clusterName }}</code
            ><input v-model="confirmation" autocomplete="off" /></label
          ><button
            class="danger"
            :disabled="busy || confirmation !== clusterName"
          >
            {{ t("Восстановить через задание") }}
          </button>
        </form></template
      >
      <details v-else-if="isAdmin">
        <summary>{{ t("Удалить резервную копию") }}</summary>
        <form class="settings-form" @submit.prevent="remove">
          <label
            >{{ t("Введите имя файла для подтверждения")
            }}<input v-model="confirmation" autocomplete="off" /></label
          ><button
            class="danger"
            :disabled="busy || confirmation !== selected.filename"
          >
            {{ t("Удалить") }}
          </button>
        </form>
      </details>
      <p v-if="error" class="notice error">{{ error }}</p></Modal
    >
  </section>
</template>

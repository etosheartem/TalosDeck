<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from "vue";
import { request, post, download } from "./client";
import { t } from "./i18n";
const props = defineProps<{ node: string; config: string }>();
const emit = defineEmits<{ submitted: [] }>();
const patch = ref("");
const mode = ref("auto");
const plan = ref<any>(null);
const restorePlan = ref(false);
const revisions = ref<any[]>([]);
const revision = ref<any>(null);
const confirmed = ref("");
const error = ref("");
const busy = ref(false);
let live = true;
const base = `/config/${encodeURIComponent(props.node)}`;
async function run(action: () => Promise<void>) {
  if (busy.value || !props.node) return;
  busy.value = true;
  error.value = "";
  try {
    await action();
  } catch (e) {
    if (live) error.value = e instanceof Error ? e.message : String(e);
  } finally {
    if (live) busy.value = false;
  }
}
async function history() {
  const result = await request(`${base}/history`);
  if (live)
    revisions.value = Array.isArray(result) ? result : result.revisions || [];
}
watch([patch, mode], () => {
  plan.value = null;
  confirmed.value = "";
});
onMounted(() => run(history));
onUnmounted(() => {
  live = false;
  patch.value = "";
});
async function preview() {
  await run(async () => {
    const result = await post(`${base}/plan`, {
      patch: patch.value,
      mode: mode.value,
    });
    if (live) {
      plan.value = result;
      restorePlan.value = false;
      revision.value = null;
    }
  });
}
async function apply() {
  await run(async () => {
    await post(`${base}/apply`, {
      planId: plan.value.id,
      confirmedNode: confirmed.value,
    });
    if (live) {
      patch.value = "";
      emit("submitted");
    }
  });
}
async function inspect(id: string) {
  await run(async () => {
    const result = await request(`${base}/history/${encodeURIComponent(id)}`);
    if (live) {
      revision.value = result;
      plan.value = null;
      confirmed.value = "";
    }
  });
}
async function previewRestore() {
  await run(async () => {
    const result = await post(`${base}/restore-plan`, {
      revisionId: revision.value.id,
      mode: mode.value,
    });
    if (live) {
      plan.value = result;
      restorePlan.value = true;
      confirmed.value = "";
    }
  });
}
async function restore() {
  await run(async () => {
    await post(`${base}/restore`, {
      planId: plan.value.id,
      confirmedNode: confirmed.value,
    });
    if (live) emit("submitted");
  });
}
</script>
<template>
  <div class="config-editor">
    <section class="code-surface">
      <header class="surface-heading">
        <h2>MachineConfig</h2>
        <button
          :disabled="!config"
          @click="download(config, `${node}-machineconfig.yaml`)"
        >
          {{ t("Скачать YAML") }}
        </button>
      </header>
      <details>
        <summary>{{ t("Текущая конфигурация") }}</summary>
        <pre class="config-code">{{ config }}</pre>
      </details>
      <footer>{{ t("Закрытые ключи маскируются сервером.") }}</footer>
    </section>
    <form class="settings-form config-patch" @submit.prevent="preview">
      <h2>{{ t("Изменить конфигурацию") }}</h2>
      <label
        >{{ t("YAML patch")
        }}<textarea
          v-model="patch"
          rows="10"
          spellcheck="false"
          :disabled="busy"
          required
          placeholder="apiVersion: v1alpha1&#10;kind: KubeNodeConfig&#10;labels:&#10;  environment: production"
        />
      </label>
      <label
        >{{ t("Режим применения")
        }}<select v-model="mode" :disabled="busy">
          <option value="auto">auto</option>
          <option value="reboot">reboot</option>
          <option value="staged">staged</option>
        </select></label
      >
      <button :disabled="busy || !node || !patch.trim()">
        {{ t("Проверить и сравнить") }}
      </button>
    </form>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <section v-if="plan || revision" class="code-surface">
      <header class="surface-heading">
        <h2>{{ plan ? t("Проверка изменений") : t("Ревизия") }}</h2>
      </header>
      <pre class="config-code">{{
        (plan || revision).diff || t("Нет изменений")
      }}</pre>
      <p
        v-for="warning in plan?.warnings || []"
        :key="warning"
        class="notice warning"
      >
        {{ warning }}
      </p>
      <details v-if="revision">
        <summary>{{ t("Конфигурация ревизии") }}</summary>
        <pre class="config-code">{{ revision.config }}</pre>
      </details>
      <div v-if="revision && !plan" class="settings-form">
        <button :disabled="busy" @click="previewRestore">
          {{ t("Проверить восстановление") }}
        </button>
      </div>
      <form
        v-if="plan"
        class="settings-form"
        @submit.prevent="restorePlan ? restore() : apply()"
      >
        <p>{{ t("Для подтверждения введите адрес ноды: {0}", [node]) }}</p>
        <label
          >{{ t("Адрес ноды")
          }}<input v-model="confirmed" autocomplete="off" :disabled="busy"
        /></label>
        <button class="primary" :disabled="busy || confirmed !== node">
          {{
            restorePlan
              ? t("Восстановить через задание")
              : t("Применить через задание")
          }}
        </button>
      </form>
    </section>
    <section class="code-surface">
      <header class="surface-heading">
        <h2>{{ t("История конфигурации") }}</h2>
        <button :disabled="busy" @click="run(history)">
          {{ t("Обновить") }}
        </button>
      </header>
      <table class="config-history">
        <thead>
          <tr>
            <th>{{ t("Время") }}</th>
            <th>{{ t("Пользователь") }}</th>
            <th>{{ t("Режим применения") }}</th>
            <th>{{ t("Состояние") }}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in revisions" :key="item.id">
            <td>{{ item.createdAt }}</td>
            <td>{{ item.author }}</td>
            <td>{{ item.mode }}</td>
            <td>{{ item.status }}</td>
            <td>
              <button :disabled="busy" @click="inspect(item.id)">
                {{ t("Просмотреть") }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-if="!revisions.length" class="footnote">
        {{ t("Ревизий пока нет") }}
      </p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { request, post } from "./client";
import { t } from "./i18n";
import { isAdmin } from "./permissions";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
const rows = ref<any[]>([]),
  busy = ref(false),
  error = ref(""),
  adding = ref(false),
  selected = ref<any>(null),
  confirmed = ref("");
const defaults = () => ({
  name: "",
  kind: "proxmox",
  config: {
    baseUrl: "",
    node: "",
    apiToken: "",
    username: "",
    password: "",
    caCert: "",
    skipTlsVerify: false,
    defaultStorage: "local-lvm",
    defaultISO: "",
    defaultBridge: "vmbr0",
  },
});
const form = ref(defaults());
let live = true;
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
  const value = await request("/providers");
  if (live) rows.value = Array.isArray(value) ? value : value.providers || [];
}
async function save() {
  await run(async () => {
    await post("/providers", form.value);
    if (!live) return;
    adding.value = false;
    form.value = defaults();
    await load();
  });
}
async function remove() {
  if (confirmed.value !== selected.value.name) return;
  await run(async () => {
    await request(`/providers/${selected.value.id}`, { method: "DELETE" });
    if (!live) return;
    selected.value = null;
    await load();
  });
}
onMounted(() => run(load));
onUnmounted(() => {
  live = false;
  form.value = defaults();
});
</script>
<template>
  <section class="panel">
    <header>
      <h2>{{ t("Подключения") }}</h2>
      <div class="toolbar">
        <button :disabled="busy" @click="run(load)">{{ t("Обновить") }}</button
        ><button v-if="isAdmin" class="primary" @click="adding = true">
          {{ t("Добавить провайдера") }}
        </button>
      </div>
    </header>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <div v-if="busy && !rows.length" class="loading-state">
      {{ t("Загрузка…") }}
    </div>
    <ResourceTable
      v-else
      :rows="rows"
      :columns="[
        { key: 'name', title: t('Имя') },
        { key: 'kind', title: t('Тип') },
        { key: 'baseUrl', title: t('Адрес'), mono: true },
        { key: 'node', title: t('Нода') },
        { key: 'defaultStorage', title: t('Хранилище') },
      ]"
      @select="
        selected = $event;
        confirmed = '';
      "
    />
    <Modal
      v-if="adding"
      :title="t('Добавить провайдера')"
      wide
      @close="
        () => {
          if (!busy) {
            adding = false;
            form = defaults();
          }
        }
      "
      ><form class="settings-form" @submit.prevent="save">
        <div class="form-grid">
          <label>{{ t("Имя") }}<input v-model="form.name" required /></label
          ><label
            >Proxmox URL<input
              v-model="form.config.baseUrl"
              type="url"
              placeholder="https://pve.example:8006"
              required /></label
          ><label
            >{{ t("Нода Proxmox")
            }}<input v-model="form.config.node" required /></label
          ><label
            >API token<input
              v-model="form.config.apiToken"
              type="password"
              autocomplete="new-password"
              placeholder="user@realm!token=secret"
          /></label>
        </div>
        <details>
          <summary>{{ t("Вход по паролю вместо токена") }}</summary>
          <div class="form-grid">
            <label
              >{{ t("Пользователь")
              }}<input
                v-model="form.config.username"
                autocomplete="off" /></label
            ><label
              >{{ t("Пароль")
              }}<input
                v-model="form.config.password"
                type="password"
                autocomplete="new-password"
            /></label>
          </div>
        </details>
        <div class="form-grid">
          <label
            >{{ t("Хранилище")
            }}<input v-model="form.config.defaultStorage" required /></label
          ><label
            >{{ t("Сетевой мост")
            }}<input v-model="form.config.defaultBridge" required /></label
          ><label
            >Talos ISO<input
              v-model="form.config.defaultISO"
              placeholder="local:iso/talos-nocloud.iso"
          /></label>
        </div>
        <details>
          <summary>TLS</summary>
          <label
            >CA PEM<textarea
              v-model="form.config.caCert"
              rows="5"
              spellcheck="false"
            /></label
          ><label class="check-label"
            ><input v-model="form.config.skipTlsVerify" type="checkbox" />{{
              t("Отключить проверку сертификата TLS")
            }}</label
          >
        </details>
        <p v-if="error" class="notice error">{{ error }}</p>
        <button
          class="primary"
          :disabled="
            busy ||
            (!form.config.apiToken &&
              (!form.config.username || !form.config.password))
          "
        >
          {{ t("Подключить") }}
        </button>
      </form></Modal
    >
    <Modal
      v-if="selected"
      :title="selected.name"
      @close="!busy && (selected = null)"
      ><dl class="detail-grid">
        <template
          v-for="key in [
            'kind',
            'baseUrl',
            'node',
            'defaultStorage',
            'defaultBridge',
            'defaultISO',
          ]"
          :key="key"
          ><dt>{{ key }}</dt>
          <dd>{{ selected[key] || "—" }}</dd></template
        >
      </dl>
      <form v-if="isAdmin" class="settings-form" @submit.prevent="remove">
        <p>
          {{
            t(
              "Удаляется подключение. Провайдер с зарегистрированными машинами удалить нельзя.",
            )
          }}
        </p>
        <label
          >{{ t("Введите имя для подтверждения")
          }}<input v-model="confirmed" autocomplete="off" /></label
        ><button class="danger" :disabled="busy || confirmed !== selected.name">
          {{ t("Удалить подключение") }}
        </button>
        <p v-if="error" class="notice error">{{ error }}</p>
      </form></Modal
    >
  </section>
</template>

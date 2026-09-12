<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { request, post } from "./client";
import { t } from "./i18n";
import { isAdmin } from "./permissions";
import { currentUser } from "../api";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
const users = ref<any[]>([]),
  error = ref(""),
  busy = ref(false),
  notice = ref("");
const editing = ref<any>(null),
  creating = ref(false);
const form = ref({ username: "", password: "", role: "viewer" });
const self = ref({ currentPassword: "", password: "" });
const replacement = ref("");
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
  if (!isAdmin.value) return;
  const value = await request("/auth/users");
  if (live) users.value = value.users || [];
}
async function create() {
  await run(async () => {
    await post("/auth/users", form.value);
    if (!live) return;
    creating.value = false;
    form.value = { username: "", password: "", role: "viewer" };
    await load();
  });
}
async function update() {
  await run(async () => {
    await request(`/auth/users/${editing.value.id}`, {
      method: "PATCH",
      body: JSON.stringify({
        ...(editing.value.provider === 'local' ? {role: editing.value.role} : {}),
        disabled: editing.value.disabled,
      }),
    });
    await load();
    if (live) editing.value = null;
  });
}
async function changePassword() {
  await run(async () => {
    await post("/auth/password", self.value);
    if (live) {
      self.value = { currentPassword: "", password: "" };
      notice.value = t("Пароль изменён");
    }
  });
}
async function reset() {
  await run(async () => {
    await post(`/auth/users/${editing.value.id}/password`, {
      password: replacement.value,
    });
    if (live) {
      replacement.value = "";
      notice.value = t("Пароль изменён");
    }
  });
}
async function revoke() {
  await run(async () => {
    await post(`/auth/users/${editing.value.id}/revoke`);
    if (live) notice.value = t("Сеансы пользователя отозваны");
  });
}
onMounted(() => run(load));
onUnmounted(() => {
  live = false;
  form.value.password = "";
  replacement.value = "";
  self.value = { currentPassword: "", password: "" };
});
</script>
<template>
  <div class="module-stack">
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <section v-if="isAdmin" class="panel">
      <header>
        <h2>{{ t("Пользователи") }}</h2>
        <div class="toolbar">
          <button :disabled="busy" @click="run(load)">
            {{ t("Обновить") }}</button
          ><button class="primary" @click="creating = true">
            {{ t("Добавить пользователя") }}
          </button>
        </div>
      </header>
      <ResourceTable
        :rows="
          users.map((u) => ({
            ...u,
            status: u.disabled ? t('Отключён') : t('Активен'),
          }))
        "
        :columns="[
          { key: 'username', title: t('Пользователь') },
          { key: 'role', title: t('Роль') },
          { key: 'provider', title: t('Вход') },
          { key: 'status', title: t('Состояние') },
        ]"
        @select="
          editing = { ...$event };
          replacement = '';
        "
      />
      <p class="footnote">
        {{
          t(
            "Viewer читает данные. Operator выполняет обслуживание. Administrator управляет доступом, конфигурацией и инфраструктурой.",
          )
        }}
      </p>
    </section>
    <section class="panel">
      <header>
        <h2>{{ t("Моя учётная запись") }}</h2>
      </header>
      <div class="settings-form">
        <p>
          {{ currentUser.username }} · {{ currentUser.role }} ·
          {{ currentUser.provider || "local" }}
        </p>
        <form
          v-if="currentUser.provider !== 'oidc'"
          class="settings-form"
          @submit.prevent="changePassword"
        >
          <label
            >{{ t("Текущий пароль")
            }}<input
              v-model="self.currentPassword"
              type="password"
              autocomplete="current-password"
              required /></label
          ><label
            >{{ t("Новый пароль")
            }}<input
              v-model="self.password"
              type="password"
              autocomplete="new-password"
              minlength="12"
              required /></label
          ><button :disabled="busy">{{ t("Изменить пароль") }}</button>
        </form>
        <p v-else>
          {{ t("Паролем и вторым фактором управляет ваш провайдер входа.") }}
        </p>
      </div>
    </section>
    <Modal
      v-if="creating"
      :title="t('Добавить пользователя')"
      @close="
        !busy && (creating = false);
        form.password = '';
      "
      ><form class="settings-form" @submit.prevent="create">
        <label
          >{{ t("Пользователь")
          }}<input v-model="form.username" autocomplete="off" required /></label
        ><label
          >{{ t("Пароль")
          }}<input
            v-model="form.password"
            type="password"
            autocomplete="new-password"
            minlength="12"
            required /></label
        ><label
          >{{ t("Роль")
          }}<select v-model="form.role" :aria-label="t('Роль')">
            <option>viewer</option>
            <option>operator</option>
            <option>admin</option>
          </select></label
        >
        <p v-if="error" class="notice error">{{ error }}</p>
        <button class="primary" :disabled="busy">{{ t("Создать") }}</button>
      </form></Modal
    >
    <Modal
      v-if="editing"
      :title="editing.username"
      @close="
        !busy && (editing = null);
        replacement = '';
      "
      ><form class="settings-form" @submit.prevent="update">
        <label
          >{{ t("Роль")
          }}<select v-model="editing.role" :aria-label="t('Роль')" :disabled="editing.provider !== 'local'">
            <option>viewer</option>
            <option>operator</option>
            <option>admin</option>
          </select></label
        ><label class="check-label"
          ><input v-model="editing.disabled" type="checkbox" />{{
            t("Отключить пользователя")
          }}</label
        ><button :disabled="busy">{{ t("Сохранить") }}</button>
      </form>
      <form
        v-if="editing.provider === 'local'"
        class="settings-form"
        @submit.prevent="reset"
      >
        <label
          >{{ t("Новый пароль")
          }}<input
            v-model="replacement"
            type="password"
            autocomplete="new-password"
            minlength="12"
            required /></label
        ><button :disabled="busy">{{ t("Изменить пароль") }}</button>
      </form>
      <div class="settings-form">
        <button :disabled="busy" @click="revoke">
          {{ t("Отозвать сеансы") }}
        </button>
        <p v-if="error" class="notice error">{{ error }}</p>
        <p v-if="notice" role="status">{{ notice }}</p>
      </div></Modal
    >
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { request as apiRequest } from "./client";
import { t } from "./i18n";
import { isAdmin } from "./permissions";
import ResourceTable from "./ResourceTable.vue";
import Modal from "./Modal.vue";
const emit = defineEmits<{ add: []; replace: []; submitted: [] }>();
const props = defineProps<{global?:boolean}>();
const request = (path:string, init:RequestInit={}) => apiRequest(path,init,props.global?'global':'cluster');
const post = (path:string,body:unknown) => request(path,{method:'POST',body:JSON.stringify(body)});
const rows = ref<any[]>([]),
  error = ref(""),
  busy = ref(false),
  selected = ref<any>(null),
  plan = ref<any>(null),
  confirmation = ref("");
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
  const result = await request("/machines");
  if (live) rows.value = Array.isArray(result) ? result : result.machines || [];
}
async function preview() {
  await run(async () => {
    const result = await post("/provision/plan", {
      kind: props.global ? 'machine-cleanup' : "worker-delete",
      name: selected.value.name,
      providerId: selected.value.providerId,
      machineId: selected.value.id,
    });
    if (live) {
      plan.value = result;
      confirmation.value = "";
    }
  });
}
async function remove() {
  if (!plan.value || confirmation.value !== plan.value.spec.name) return;
  await run(async () => {
    await post("/provision", {
      planId: plan.value.id,
      confirmedName: confirmation.value,
    });
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
      <h2>{{ t("Управляемые машины") }}</h2>
      <div class="toolbar">
        <button :disabled="busy" @click="run(load)">{{ t("Обновить") }}</button
        ><button v-if="isAdmin && !global" @click="emit('replace')">{{t('Заменить worker')}}</button><button v-if="isAdmin && !global" class="primary" @click="emit('add')">
          {{ t("Добавить worker") }}
        </button>
      </div>
    </header>
    <p class="footnote">
      {{
        t(
          "Здесь показаны только машины, созданные TalosDeck. Существующие ноды доступны в разделе «Ноды».",
        )
      }}
    </p>
    <p v-if="global" class="footnote">{{ t('Ресурсы незавершённого создания кластера. Очистка доступна только после остановки задания и проверки принадлежности машины.') }}</p>
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <ResourceTable
      :rows="rows"
      :columns="[
        { key: 'name', title: t('Имя') },
        { key: 'role', title: t('Роль') },
        ...(global ? [{key:'clusterId',title:t('Кластер')}] : []),
        { key: 'status', title: t('Состояние') },
        { key: 'address', title: t('Адрес') },
        { key: 'vmid', title: 'VMID' },
        { key: 'providerNode', title: 'Proxmox' },
      ]"
      @select="
        selected = $event;
        plan = null;
      "
    /><Modal
      v-if="selected"
      :title="selected.name"
      @close="!busy && (selected = null)"
      ><dl class="detail-grid">
        <template
          v-for="key in [
            'role',
            'status',
            'address',
            'vmid',
            'providerNode',
            'mac',
          ]"
          :key="key"
          ><dt>{{ key }}</dt>
          <dd>{{ selected[key] || "—" }}</dd></template
        >
      </dl>
      <button
        v-if="isAdmin && !plan && (global ? selected.cleanupEligible === true : selected.role === 'worker')"
        :disabled="busy"
        @click="preview"
      >
        {{ t("Проверить удаление") }}</button
      ><template v-if="plan"
        ><p
          v-for="note in plan.safetyNotes || []"
          :key="note"
          class="notice warning"
        >
          {{ note }}
        </p>
        <form class="settings-form" @submit.prevent="remove">
          <label
            >{{ t("Введите имя для подтверждения")
            }}<code>{{ plan.spec.name }}</code
            ><input v-model="confirmation" autocomplete="off" /></label
          ><button
            class="danger"
            :disabled="busy || confirmation !== plan.spec.name"
          >
            {{ t("Удалить через задание") }}
          </button>
        </form></template
      >
      <p v-if="error" class="notice error">{{ error }}</p></Modal
    >
  </section>
</template>

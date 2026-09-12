<script setup lang="ts">
import { t } from "./i18n";

import { ref, watch, onUnmounted, nextTick } from "vue";
import type { NodeOverview } from "../types";
import { getAuthToken } from "../api";
import { canOperate } from "./permissions";
import { clusterWebSocket } from "../clusterScope";
import { request, globalRequest, post, download, list } from "./client";
import ResourceTable from "./ResourceTable.vue";
const props = defineProps<{
  node: NodeOverview;
  initialTab?: "services" | "logs";
}>();
const emit = defineEmits<{ changed: []; workloads: []; storage: [] }>();
const tab = ref<string>(props.initialTab || "services");
const rows = ref<any[]>([]);
const extensionInfo=ref<any>(null);
const targetVersions=ref<string[]>([]),targetVersion=ref(''),compatibility=ref<any>(null),compatibilityError=ref(''),compatibilityBusy=ref(false),catalogStale=ref(false);
watch(targetVersion,()=>{compatibility.value=null;compatibilityError.value='';});
async function loadTargetVersions(id:number){compatibilityBusy.value=false;targetVersions.value=[];targetVersion.value='';compatibility.value=null;compatibilityError.value='';catalogStale.value=false;try{const r=await globalRequest('/images/versions');if(id===generation){targetVersions.value=r.versions||[];catalogStale.value=!!r.stale;targetVersion.value=targetVersions.value[0]||'';}}catch(e){if(id===generation)compatibilityError.value=String(e);}}
async function checkCompatibility(){if(!extensionInfo.value?.schematicId||!targetVersion.value||catalogStale.value||compatibilityBusy.value)return;const id=generation;compatibility.value=null;compatibilityError.value='';compatibilityBusy.value=true;try{const r=await globalRequest('/images/schematics/'+encodeURIComponent(extensionInfo.value.schematicId)+'?version='+encodeURIComponent(targetVersion.value));if(id===generation)compatibility.value=r;}catch(e){if(id===generation)compatibilityError.value=String(e);}finally{if(id===generation)compatibilityBusy.value=false;}}

const error = ref("");
const loading = ref(false);
const logs = ref<string[]>([]);
const paused = ref(false);
const filter = ref("");
const status = ref("Отключено");
const viewport = ref<HTMLElement>();
const service = ref("dmesg");
const autoscroll = ref(true);
let socket: WebSocket | null = null;
let generation = 0;
const detail = ref<any>(null);
const pending = ref("");
const busy = ref(false);
function closeSocket() {
  if (socket) {
    socket.onclose = null;
    socket.onerror = null;
    socket.onmessage = null;
    socket.close();
    socket = null;
  }
}
function connect() {
  closeSocket();
  logs.value = [];
  status.value = "Подключение…";
  const token = getAuthToken();
  if (!token) {
    error.value = t("Войдите для просмотра живого потока");
    return;
  }
  const path =
    service.value === "dmesg"
      ? "dmesg"
      : `logs/${encodeURIComponent(service.value)}`;
  socket = new WebSocket(
    `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${clusterWebSocket(`/nodes/${encodeURIComponent(props.node.ip)}/${path}`)}`,
    [token],
  );
  socket.onopen = () => (status.value = "Подключено");
  socket.onclose = () => (status.value = "Отключено");
  socket.onerror = () => {
    error.value = t(
      "Не удалось подключиться к потоку. Проверьте авторизацию и доступность ноды.",
    );
  };
  socket.onmessage = (e) => {
    if (paused.value) return;
    logs.value.push(String(e.data));
    if (logs.value.length > 2000)
      logs.value.splice(0, logs.value.length - 2000);
    if (autoscroll.value)
      nextTick(() => {
        if (viewport.value)
          viewport.value.scrollTop = viewport.value.scrollHeight;
      });
  };
}
async function load() {
  const id = ++generation;
  error.value = "";
  if(tab.value==='extensions')extensionInfo.value=null;
  closeSocket();
  if (tab.value === "logs") {
    connect();
    return;
  }
  loading.value = true;
  try {
    const data = await request(
      `/nodes/${encodeURIComponent(props.node.ip)}/${tab.value}`,
    );
    if (id === generation && tab.value==='extensions'){extensionInfo.value=data;rows.value=data.extensions||[];loadTargetVersions(id);}
    else if (id === generation) rows.value = list(data).map(row=>tab.value==='services' && row.healthKnown===false ? {...row,healthy:t('Неизвестно')} : row);
  } catch (e) {
    if (id === generation) error.value = String(e);
  } finally {
    if (id === generation) loading.value = false;
  }
}
watch(tab, load, { immediate: true });
onUnmounted(() => {
  generation++;
  closeSocket();
});
async function restart() {
  if (!pending.value) return;
  busy.value = true;
  try {
    await post(
      `/nodes/${encodeURIComponent(props.node.ip)}/services/${encodeURIComponent(pending.value)}/restart`,
    );
    pending.value = "";
    await load();
    emit("changed");
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <div class="inspector-meta">
    <span class="mono">{{ node.ip }}</span
    ><span>{{ node.role || t("Роль неизвестна") }}</span
    ><span>Talos {{ node.version || "—" }}</span
    ><span>CPU {{ node.cpuUsage ?? "—" }}%</span
    ><span>RAM {{ node.memoryUsage || "—" }}</span>
  </div>
  <div class="toolbar"><button @click="emit('workloads')">{{ t('Рабочие нагрузки ноды') }}</button><button @click="emit('storage')">{{ t('Диски ноды') }}</button></div>
  <nav class="section-tabs">
    <button
      v-for="item in [
        ['services', t('Сервисы')],
        ['containers', t('Контейнеры')],
        ['logs', t('Живые логи')],
        ['extensions',t('Расширения')],
      ]"
      :key="item[0]"
      :class="{ selected: tab === item[0] }"
      @click="tab = item[0]!"
    >
      {{ item[1] }}
    </button>
  </nav>
  <div v-if="error" class="notice error" role="alert">{{ error }}</div>
  <div v-if="loading" class="loading-state">{{ t("Загрузка…") }}</div>
  <template v-else-if="tab === 'logs'"
    ><div class="toolbar">
      <select
        v-model="service"
        :aria-label="t('Источник логов')"
        @change="connect"
      >
        <option>dmesg</option>
        <option>kubelet</option>
        <option>etcd</option>
        <option>containerd</option>
        <option>apid</option></select
      ><span class="state muted">{{ t(status) }}</span
      ><button @click="connect">{{ t("Подключить") }}</button
      ><button @click="paused = !paused">
        {{ paused ? t("Продолжить") : t("Пауза") }}</button
      ><button @click="logs = []">{{ t("Очистить") }}</button
      ><button @click="download(logs.join('\n'), `${node.hostname}.log`)">
        {{ t("Экспорт") }}
      </button>
    </div>
    <div class="toolbar">
      <input v-model="filter" :placeholder="t('Фильтр строк')" /><label
        ><input v-model="autoscroll" type="checkbox" /> {{ t("Автопрокрутка") }}
      </label>
    </div>
    <pre ref="viewport" class="log-view">{{
      logs
        .filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
        .join("\n") || t("Ожидание сообщений…")
    }}</pre>
  </template>
  <template v-else-if="tab==='extensions'"><div class="toolbar"><button :disabled="loading" @click="load">{{ t('Обновить') }}</button></div><p class="footnote">{{ t('Изменение набора расширений требует обновления образа и перезагрузки. Текущее наблюдение не определяет наличие отложенных изменений.') }}</p><p v-if="!extensionInfo" class="notice warning">{{ t('Состояние расширений неизвестно') }}</p><template v-else><p v-if="extensionInfo.consistent===false" class="notice warning">{{ t('Настроенный installer не соответствует наблюдаемому образу или расширениям. Проверьте конфигурацию перед обновлением.') }}</p><dl class="detail-grid"><dt>Schematic ID</dt><dd class="mono">{{ extensionInfo.schematicId||t('Неизвестно') }}</dd><dt>Installer</dt><dd class="mono">{{ extensionInfo.installerImage||t('Неизвестно') }}</dd><dt>{{ t('Последняя проверка') }}</dt><dd>{{ extensionInfo.checkedAt||'—' }}</dd></dl><ResourceTable :rows="rows" :empty="t('Установленных расширений нет')" :columns="[{key:'name',title:t('Расширение')},{key:'version',title:t('Версия')}]" /><section class="surface"><h3>{{ t('Совместимость текущего schematic') }}</h3><label>{{ t('Целевая версия Talos') }}<select v-model="targetVersion" :aria-label="t('Целевая версия Talos')" :disabled="compatibilityBusy||catalogStale"><option v-for="version in targetVersions" :key="version">{{ version }}</option></select></label><button :disabled="compatibilityBusy||catalogStale||!targetVersion||!extensionInfo.schematicId||extensionInfo.consistent===false" @click="checkCompatibility">{{ t('Проверить доступность образа') }}</button><p v-if="catalogStale" class="notice warning">{{ t('Каталог устарел. Создание профиля заблокировано до успешного обновления.') }}</p><p v-if="compatibilityError" class="notice error">{{ compatibilityError }}</p><p v-if="compatibility" class="notice">{{ t('Image Factory подтвердил образ текущего schematic для версии {0}. План обновления всё равно должен проверить здоровье и совместимость кластера.',[compatibility.version]) }}</p></section></template></template>
  <template v-else
    ><ResourceTable
      :rows="rows"
      :columns="
        tab === 'services'
          ? [
              { key: 'id', title: t('Сервис'), mono: true },
              { key: 'state', title: t('Состояние') },
              { key: 'healthy', title: t('Здоровье') },
              { key: 'description', title: t('Описание') },
            ]
          : [
              { key: 'id', title: t('Контейнер'), mono: true },
              { key: 'name', title: t('Имя') },
              { key: 'status', title: t('Состояние') },
              { key: 'image', title: t('Образ') },
            ]
      "
      @select="detail = $event"
    />
    <section v-if="detail" class="inline-details">
      <pre>{{ JSON.stringify(detail, null, 2) }}</pre>
      <button
        v-if="tab === 'services'"
        :disabled="!canOperate"
        @click="pending = detail.id"
      >
        {{ t("Перезапустить сервис") }}
      </button>
    </section>
    <div v-if="pending" class="notice warning">
      <p>{{ t("Перезапустить {0} на {1}?", [pending, node.hostname]) }}</p>
      <button :disabled="busy" @click="pending = ''">{{ t("Отмена") }}</button
      ><button class="danger" :disabled="busy" @click="restart">
        {{ t("Подтвердить перезапуск") }}
      </button>
    </div></template
  >
</template>

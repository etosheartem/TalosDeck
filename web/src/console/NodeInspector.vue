<script setup lang="ts">
import { ref, watch, onUnmounted, nextTick } from "vue";
import type { NodeOverview } from "../types";
import { getAuthToken, isAuthenticated } from "../api";
import { request, post, download, list } from "./client";
import ResourceTable from "./ResourceTable.vue";
const props = defineProps<{ node: NodeOverview }>();
const emit = defineEmits<{ changed: [] }>();
const tab = ref("services");
const rows = ref<any[]>([]);
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
    error.value = "Войдите для просмотра живого потока";
    return;
  }
  const path =
    service.value === "dmesg"
      ? "dmesg"
      : `logs/${encodeURIComponent(service.value)}`;
  socket = new WebSocket(
    `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/ws/nodes/${encodeURIComponent(props.node.ip)}/${path}`,
    [token],
  );
  socket.onopen = () => (status.value = "Подключено");
  socket.onclose = () => (status.value = "Отключено");
  socket.onerror = () => {
    error.value =
      "Не удалось подключиться к потоку. Проверьте авторизацию и доступность ноды.";
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
    if (id === generation) rows.value = list(data);
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
    ><span>{{ node.role || "Роль неизвестна" }}</span
    ><span>Talos {{ node.version || "—" }}</span
    ><span>CPU {{ node.cpuUsage ?? "—" }}%</span
    ><span>RAM {{ node.memoryUsage || "—" }}</span>
  </div>
  <nav class="section-tabs">
    <button
      v-for="item in [
        ['services', 'Сервисы'],
        ['containers', 'Контейнеры'],
        ['logs', 'Живые логи'],
      ]"
      :key="item[0]"
      :class="{ selected: tab === item[0] }"
      @click="tab = item[0]!"
    >
      {{ item[1] }}
    </button>
  </nav>
  <div v-if="error" class="notice error" role="alert">{{ error }}</div>
  <div v-if="loading" class="loading-state">Загрузка…</div>
  <template v-else-if="tab === 'logs'"
    ><div class="toolbar">
      <select v-model="service" aria-label="Источник логов" @change="connect">
        <option>dmesg</option>
        <option>kubelet</option>
        <option>etcd</option>
        <option>containerd</option>
        <option>apid</option></select
      ><span class="state muted">{{ status }}</span
      ><button @click="connect">Подключить</button
      ><button @click="paused = !paused">
        {{ paused ? "Продолжить" : "Пауза" }}</button
      ><button @click="logs = []">Очистить</button
      ><button @click="download(logs.join('\n'), `${node.hostname}.log`)">
        Экспорт
      </button>
    </div>
    <div class="toolbar">
      <input v-model="filter" placeholder="Фильтр строк" /><label
        ><input v-model="autoscroll" type="checkbox" /> Автопрокрутка</label
      >
    </div>
    <pre ref="viewport" class="log-view">{{
      logs
        .filter((l) => l.toLowerCase().includes(filter.toLowerCase()))
        .join("\n") || "Ожидание сообщений…"
    }}</pre>
  </template>
  <template v-else
    ><ResourceTable
      :rows="rows"
      :columns="
        tab === 'services'
          ? [
              { key: 'id', title: 'Сервис', mono: true },
              { key: 'state', title: 'Состояние' },
              { key: 'healthy', title: 'Здоровье' },
              { key: 'description', title: 'Описание' },
            ]
          : [
              { key: 'id', title: 'Контейнер', mono: true },
              { key: 'name', title: 'Имя' },
              { key: 'status', title: 'Состояние' },
              { key: 'image', title: 'Образ' },
            ]
      "
      @select="detail = $event"
    />
    <section v-if="detail" class="inline-details">
      <pre>{{ JSON.stringify(detail, null, 2) }}</pre>
      <button
        v-if="tab === 'services'"
        :disabled="!isAuthenticated"
        @click="pending = detail.id"
      >
        Перезапустить сервис
      </button>
    </section>
    <div v-if="pending" class="notice warning">
      <p>Перезапустить {{ pending }} на {{ node.hostname }}?</p>
      <button :disabled="busy" @click="pending = ''">Отмена</button
      ><button class="danger" :disabled="busy" @click="restart">
        Подтвердить перезапуск
      </button>
    </div></template
  >
</template>

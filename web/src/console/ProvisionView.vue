<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from "vue";
import { request, globalRequest, post, globalPost } from "./client";
import { t } from "./i18n";
import ResourceTable from "./ResourceTable.vue";
const props = defineProps<{
  kind: "cluster-create" | "worker-create";
  clusterName?: string;
}>();
const emit = defineEmits<{ submitted: [any] }>();
const providers = ref<any[]>([]),
  busy = ref(false),
  error = ref(""),
  plan = ref<any>(null),
  confirmation = ref("");
const spec = ref({
  kind: props.kind,
  name: props.kind === "worker-create" ? props.clusterName || "" : "",
  providerId: "",
  talosVersion: "",
  kubernetesVersion: "",
  installerImage: "",
  endpoint: "",
  ...(props.kind === 'cluster-create' ? {cni:'flannel',storage:'none'} : {}),
  machines: [] as any[],
});
let live = true;
function add(role = "worker") {
  spec.value.machines.push({
    name: `${role === "controlplane" ? "cp" : "worker"}-${String(spec.value.machines.length + 1).padStart(2, "0")}`,
    role,
    cores: role === "controlplane" ? 4 : 2,
    memoryMB: role === "controlplane" ? 4096 : 2048,
    diskGB: 40,
    storage: "",
    iso: "",
    bridge: "",
    networkMode: "dhcp",
    address: "",
    gateway: "",
    nameservers: [] as string[],
  });
}
watch(
  spec,
  () => {
    plan.value = null;
    confirmation.value = "";
  },
  { deep: true },
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
const send = (path: string, body: any) =>
  props.kind === "cluster-create" ? globalPost(path, body) : post(path, body);
async function preview() {
  await run(async () => {
    const result = await send("/provision/plan", spec.value);
    if (live) plan.value = result;
  });
}
async function submit() {
  if (!plan.value || confirmation.value !== spec.value.name) return;
  await run(async () => {
    const job = await send("/provision", {
      planId: plan.value.id,
      confirmedName: confirmation.value,
    });
    if (live) emit("submitted", job);
  });
}
onMounted(() => {
  add(props.kind === "cluster-create" ? "controlplane" : "worker");
  run(async () => {
    const value = await globalRequest("/providers");
    if (live) {
      providers.value = Array.isArray(value) ? value : value.providers || [];
      spec.value.providerId = providers.value[0]?.id || "";
      if (props.kind === "worker-create") {
        const info = await request("/cluster");
        if (live) spec.value.name = props.clusterName || info.name || "";
      }
    }
  });
});
onUnmounted(() => {
  live = false;
});
</script>
<template>
  <div class="module-stack">
    <p v-if="error" class="notice error" role="alert">{{ error }}</p>
    <form class="settings-form" @submit.prevent="preview">
      <fieldset :disabled="busy">
        <div class="form-grid">
          <label
            >{{ t("Имя кластера")
            }}<input
              v-model="spec.name"
              :readonly="kind === 'worker-create'"
              required /></label
          ><label
            >{{ t("Провайдер")
            }}<select v-model="spec.providerId" required>
              <option v-if="!providers.length" value="">
                {{ t("Сначала подключите провайдера") }}
              </option>
              <option
                v-for="provider in providers"
                :key="provider.id"
                :value="provider.id"
              >
                {{ provider.name }}
              </option>
            </select></label
          ><label
            >Talos<input
              v-model="spec.talosVersion"
              placeholder="1.x.y"
              required /></label
          ><label
            >Kubernetes<input
              v-model="spec.kubernetesVersion"
              placeholder="1.x.y"
              required /></label
          ><label class="full-width"
            >Installer image<input
              v-model="spec.installerImage"
              placeholder="factory.talos.dev/installer/SCHEMATIC:v1.x.y"
              required /></label
          ><label v-if="kind === 'cluster-create'" class="full-width"
            >Kubernetes endpoint<input
              v-model="spec.endpoint"
              type="url"
              placeholder="https://control-plane:6443"
          /></label>
          <template v-if="kind === 'cluster-create'"><label>CNI<select v-model="spec.cni" aria-label="CNI"><option value="flannel">Flannel</option><option value="cilium">Cilium</option></select></label><label>Kubernetes Storage<select v-model="spec.storage" aria-label="Kubernetes Storage"><option value="none">{{ t('Без дополнительных компонентов') }}</option><option value="local-path">Local Path</option></select></label></template>
        </div>
        <p v-if="spec.cni === 'cilium'" class="notice">{{ t('Cilium поддерживает Kubernetes 1.33–1.36. Совместимость проверяется перед созданием.') }}</p>
        <p v-if="spec.storage === 'local-path'" class="notice">{{ t('Local Path хранит данные на диске ноды без репликации. Потеря ноды может привести к потере данных.') }}</p>
        <p class="footnote">
          {{
            t(
              "ISO должен содержать qemu-guest-agent. Сеть и хранилище будут проверены перед подключением кластера.",
            )
          }}
        </p>
        <section
          v-for="(machine, index) in spec.machines"
          :key="index"
          class="machine-form"
        >
          <header>
            <strong>{{ t("Машина") }} {{ index + 1 }}</strong
            ><button
              v-if="spec.machines.length > 1"
              type="button"
              @click="spec.machines.splice(index, 1)"
            >
              {{ t("Убрать") }}
            </button>
          </header>
          <div class="form-grid">
            <label
              >{{ t("Имя") }}<input v-model="machine.name" required /></label
            ><label
              >{{ t("Роль")
              }}<select
                v-model="machine.role"
                :disabled="kind === 'worker-create'"
              >
                <option value="controlplane">Control plane</option>
                <option value="worker">Worker</option>
              </select></label
            ><label
              >CPU<input
                v-model.number="machine.cores"
                type="number"
                min="2"
                required /></label
            ><label
              >RAM (MiB)<input
                v-model.number="machine.memoryMB"
                type="number"
                min="2048"
                required /></label
            ><label
              >{{ t("Диск") }} (GiB)<input
                v-model.number="machine.diskGB"
                type="number"
                min="20"
                required /></label
            ><label
              >{{ t("Сеть")
              }}<select v-model="machine.networkMode">
                <option value="dhcp">DHCP</option>
                <option value="static">Static IPv4</option>
              </select></label
            ><template v-if="machine.networkMode === 'static'"
              ><label
                >IPv4 / CIDR<input
                  v-model="machine.address"
                  placeholder="10.0.0.10/24"
                  required /></label
              ><label
                >{{ t("Шлюз")
                }}<input v-model="machine.gateway" required /></label
              ><label
                >DNS<input
                  :value="machine.nameservers.join(', ')"
                  @input="
                    machine.nameservers = (
                      $event.target as HTMLInputElement
                    ).value
                      .split(',')
                      .map((v) => v.trim())
                      .filter(Boolean)
                  " /></label
            ></template>
          </div>
          <details>
            <summary>{{ t("Параметры провайдера") }}</summary>
            <div class="form-grid">
              <label
                >{{ t("Хранилище")
                }}<input
                  v-model="machine.storage"
                  :placeholder="t('По умолчанию')" /></label
              ><label
                >{{ t("Сетевой мост")
                }}<input
                  v-model="machine.bridge"
                  :placeholder="t('По умолчанию')" /></label
              ><label
                >ISO<input
                  v-model="machine.iso"
                  :placeholder="t('По умолчанию')" /></label
              ><label
                >VLAN<input
                  v-model.number="machine.vlan"
                  type="number"
                  min="1"
                  max="4094"
              /></label>
            </div>
          </details>
        </section>
        <div class="toolbar">
          <button type="button" @click="add()">
            {{ t("Добавить машину") }}</button
          ><button type="submit" class="primary" :disabled="!spec.providerId">
            {{ t("Проверить план") }}
          </button>
        </div>
      </fieldset>
    </form>
    <section v-if="plan" class="panel">
      <header>
        <h2>{{ t("План создания") }}</h2>
      </header>
      <ResourceTable
        :rows="plan.spec?.machines || []"
        :search="false"
        :columns="[
          { key: 'name', title: t('Имя') },
          { key: 'role', title: t('Роль') },
          { key: 'cores', title: 'CPU' },
          { key: 'memoryMB', title: 'RAM MiB' },
          { key: 'diskGB', title: 'GiB' },
        ]"
      />
      <p
        v-for="note in plan.safetyNotes || []"
        :key="note"
        class="notice warning"
      >
        {{ note }}
      </p>
      <form class="settings-form" @submit.prevent="submit">
        <label
          >{{ t("Введите имя кластера") }}<code>{{ spec.name }}</code
          ><input v-model="confirmation" autocomplete="off" /></label
        ><button class="primary" :disabled="busy || confirmation !== spec.name">
          {{ t("Создать через задание") }}
        </button>
      </form>
    </section>
  </div>
</template>

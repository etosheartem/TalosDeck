<script setup lang="ts">
import { t } from "./i18n";

import { computed, ref } from "vue";
import { ArrowDown, ArrowUp, Search } from "lucide-vue-next";
import { display } from "./client";
export interface Column {
  key: string;
  title: string;
  mono?: boolean;
}
const props = withDefaults(
  defineProps<{
    rows: any[];
    columns: Column[];
    search?: boolean;
    empty?: string;
  }>(),
  { search: true, empty: "" },
);
const emit = defineEmits<{ select: [row: any] }>();
const query = ref("");
const sort = ref("");
const ascending = ref(true);
const filtered = computed(() => {
  const result = props.rows.filter((row) =>
    JSON.stringify(row).toLowerCase().includes(query.value.toLowerCase()),
  );
  if (sort.value)
    result.sort((a, b) =>
      typeof a[sort.value] === "number" && typeof b[sort.value] === "number"
        ? (a[sort.value] - b[sort.value]) * (ascending.value ? 1 : -1)
        : display(a[sort.value]).localeCompare(
            display(b[sort.value]),
            undefined,
            { numeric: true },
          ) * (ascending.value ? 1 : -1),
    );
  return result;
});
function order(key: string) {
  ascending.value = sort.value === key ? !ascending.value : true;
  sort.value = key;
}
const tone = (v: any) =>
  ["healthy", "ready", "running", "success", "succeeded", "complete"].includes(String(v).toLowerCase())
    ? "good"
    : [
          "Failed",
          "Degraded",
          "Not Ready",
          "failed",
          "CrashLoopBackOff",
          "Error",
        ].includes(v)
      ? "bad"
      : "muted";
const healthLabel = (v:any) => ({healthy:t('Исправен'),degraded:t('Деградирован'),unknown:t('Неизвестно')})[String(v).toLowerCase()] || display(v);
</script>
<template>
  <div class="resource-table">
    <div v-if="search" class="table-tools">
      <label class="search-field"
        ><Search :size="16" /><input
          v-model="query"
          :placeholder="t('Поиск по всем полям…')"
          :aria-label="t('Поиск по таблице')" /></label
      ><span>{{ filtered.length }} {{ t("из") }} {{ rows.length }}</span
      ><slot name="tools" />
    </div>
    <div class="table-scroll" tabindex="0" :aria-label="t('Таблица ресурсов')">
      <table>
        <thead>
          <tr>
            <th
              v-for="col in columns"
              :key="col.key"
              :aria-sort="
                sort === col.key
                  ? ascending
                    ? 'ascending'
                    : 'descending'
                  : 'none'
              "
            >
              <button @click="order(col.key)">
                {{ col.title
                }}<component
                  v-if="sort === col.key"
                  :is="ascending ? ArrowUp : ArrowDown"
                  :size="12"
                />
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, i) in filtered" :key="row.id || row.ip || i">
            <td
              v-for="(col, j) in columns"
              :key="col.key"
              :class="{ mono: col.mono }"
            >
              <button
                v-if="j === 0"
                class="resource-link"
                @click="emit('select', row)"
              >
                {{ display(row[col.key]) }}</button
              ><span
                v-else-if="['status','health'].includes(col.key)"
                :class="['state', tone(row[col.key])]"
                ><i />{{ col.key === 'health' ? healthLabel(row[col.key]) : display(row[col.key]) }}</span
              ><template v-else>{{ display(row[col.key]) }}</template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="!filtered.length" class="empty-state">
      <Search :size="24" /><strong>{{
        query ? t("Ничего не найдено") : empty || t("Записей пока нет")
      }}</strong
      ><span v-if="query">{{ t("Измените запрос или сбросьте фильтр.") }}</span
      ><button v-if="query" @click="query = ''">
        {{ t("Сбросить поиск") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
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
  { search: true, empty: "Записей пока нет" },
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
  ["Healthy", "Ready", "Running", "success", "Succeeded"].includes(v)
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
</script>
<template>
  <div class="resource-table">
    <div v-if="search" class="table-tools">
      <label class="search-field"
        ><Search :size="16" /><input
          v-model="query"
          placeholder="Поиск по всем полям…"
          aria-label="Поиск по таблице" /></label
      ><span>{{ filtered.length }} из {{ rows.length }}</span
      ><slot name="tools" />
    </div>
    <div class="table-scroll" tabindex="0" aria-label="Таблица ресурсов">
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
                v-else-if="col.key === 'status'"
                :class="['state', tone(row[col.key])]"
                ><i />{{ display(row[col.key]) }}</span
              ><template v-else>{{ display(row[col.key]) }}</template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="!filtered.length" class="empty-state">
      <Search :size="24" /><strong>{{
        query ? "Ничего не найдено" : empty
      }}</strong
      ><span>{{
        query
          ? "Измените запрос или сбросьте фильтр."
          : "Данные появятся после получения от кластера."
      }}</span
      ><button v-if="query" @click="query = ''">Сбросить поиск</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, nextTick, watch } from 'vue';
import Modal from './Modal.vue';
import { t } from './i18n';
export interface Command { id: string; label: string; context: string; kind: string; search?: string; run: () => void }
const props = defineProps<{ commands: Command[]; scope: string; resourcesUnavailable?: boolean }>();
const emit = defineEmits<{close: []}>();
const query = ref(''), cursor = ref(0), input = ref<HTMLInputElement>();
const results = computed(() => props.commands.filter(c => `${c.label} ${c.context} ${c.search || ''}`.toLowerCase().includes(query.value.toLowerCase())).slice(0,30));
watch(results,()=>{cursor.value=Math.max(0,Math.min(cursor.value,results.value.length-1));});
watch(cursor,async()=>{await nextTick();document.getElementById('command-'+cursor.value)?.scrollIntoView({block:'nearest'});});
function choose(command?: Command) { if (!command) return; emit('close'); command.run(); }
function key(event: KeyboardEvent) {
 if(event.key==='ArrowDown'||event.key==='ArrowUp'){event.preventDefault();cursor.value=Math.max(0,Math.min(results.value.length-1,cursor.value+(event.key==='ArrowDown'?1:-1)));}
 if(event.key==='Enter'){event.preventDefault();choose(results.value[cursor.value]);}
}
onMounted(async()=>{await nextTick();input.value?.focus();});
</script>
<template><Modal :title="t('Быстрый переход')" @close="emit('close')"><p class="footnote">{{ scope }} · {{ t('Действия открывают проверку и подтверждение операции.') }}</p><p v-if="resourcesUnavailable" class="notice">{{ t('Список ресурсов ещё загружается или недоступен. Разделы и кластеры доступны для перехода.') }}</p><input class="command-input" ref="input" v-model="query" :aria-label="t('Найти ресурс или действие')" :placeholder="t('Раздел, нода, pod или действие')" role="combobox" aria-controls="command-results" aria-expanded="true" :aria-activedescendant="results[cursor] ? 'command-'+cursor : undefined" @input="cursor=0" @keydown="key" /><div id="command-results" role="listbox" class="command-results"><button v-for="(command,index) in results" :id="'command-'+index" :key="command.id" role="option" :aria-selected="index===cursor" class="issue-row" @click="choose(command)"><span>{{ command.label }}<small>{{ command.kind }} · {{ command.context }}</small></span></button></div><p v-if="!results.length" class="empty-state">{{ t('Совпадений нет. Поиск ресурсов ограничен выбранным кластером.') }}</p></Modal></template>

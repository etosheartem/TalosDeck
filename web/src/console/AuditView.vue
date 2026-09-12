<script setup lang="ts">
import {onMounted,onUnmounted,ref} from 'vue';
import {request} from './client';
import {t} from './i18n';
import ResourceTable from './ResourceTable.vue';
import Modal from './Modal.vue';
const props=defineProps<{global?:boolean}>();
const rows=ref<any[]>([]),total=ref(0),busy=ref(false),error=ref(''),selected=ref<any>(null);
let live=true;
async function load(){if(busy.value)return;busy.value=true;error.value='';try{const result=await request('/audit?limit=100',{},props.global?'global':'cluster');if(live){rows.value=Array.isArray(result)?result:result.events||[];total.value=result.total??rows.value.length;}}catch(e){if(live)error.value=String(e);}finally{if(live)busy.value=false;}}
onMounted(load);onUnmounted(()=>{live=false;});
</script>
<template>
<section class="panel">
<header><h2>{{ global?t('Аудит платформы'):t('Аудит кластера') }}</h2><button :disabled="busy" @click="load">{{ t('Обновить') }}</button></header>
<p class="footnote">{{ global?t('Подключения, провайдеры, пользователи и операции платформы.'):t('Действия и задания выбранного кластера.') }} {{ t('Последние {0} из {1} записей.',[rows.length,total]) }}</p>
<p v-if="error" class="notice error" role="alert">{{ error }}</p>
<div v-if="busy&&!rows.length" class="loading-state">{{ t('Загрузка…') }}</div>
<ResourceTable v-else :rows="rows" :columns="[{key:'action',title:t('Действие')},{key:'user',title:t('Пользователь')},{key:'status',title:t('Результат')},{key:'ip',title:t('Адрес'),mono:true},{key:'timestamp',title:t('Время')}]" @select="selected=$event"/>
<Modal v-if="selected" :title="selected.action" wide @close="selected=null"><pre class="detail-json">{{ JSON.stringify(selected,null,2) }}</pre></Modal>
</section>
</template>

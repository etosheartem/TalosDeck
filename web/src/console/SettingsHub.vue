<script setup lang="ts">
import { computed } from 'vue';
import { t } from './i18n';
import { isAdmin } from './permissions';
const props=defineProps<{global:boolean}>();
const emit=defineEmits<{navigate:[id:string]}>();
const items=computed(()=>props.global ? [{id:'users',title:t('Доступ'),description:t('Учётная запись, пользователи и роли')},...(isAdmin.value?[{id:'providers',title:t('Провайдеры'),description:t('Подключения и credentials инфраструктуры')},{id:'recovery-protection',title:t('Восстановление TalosDeck'),description:t('Копии панели управления, проверки восстановления и режим автоматизации')}]:[])] : [{id:'settings',title:t('Подключение'),description:t('Имя, endpoints и версии кластера')},...(isAdmin.value?[{id:'config',title:t('Конфигурация'),description:t('MachineConfig: preview, история и восстановление')},{id:'alerts',title:t('Уведомления'),description:t('Каналы и условия оповещений')}]:[])]);
</script>
<template><section class="panel"><button v-for="item in items" :key="item.id" class="issue-row" @click="emit('navigate',item.id)"><span>{{ item.title }}<small>{{ item.description }}</small></span><span aria-hidden="true">→</span></button></section></template>

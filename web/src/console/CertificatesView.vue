<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { request } from './client';
import { t } from './i18n';
import { certificateStatus, certificateDate, certificateText, type Certificate, type CertificateReport } from './certificates';
import ResourceTable from './ResourceTable.vue';
import Modal from './Modal.vue';
const report = ref<CertificateReport | null>(null), loading = ref(false), error = ref(''), selected = ref<Certificate | null>(null), filter = ref('all');
let live = true;
const rows = computed(() => (report.value?.certificates || []).filter(c => filter.value === 'all' || c.status === filter.value).map(c => ({...c, statusLabel:certificateStatus(c.status), sourceLabel:certificateText(c.source), expiry:certificateDate(c.notAfter), remaining:typeof c.daysRemaining !== 'number' ? t('Неизвестно') : c.reason === 'expired' || c.daysRemaining < 0 ? t('Истёк') : c.daysRemaining === 0 ? t('Менее суток') : t('{0} дн.', [c.daysRemaining])})));
async function load() {
  if (loading.value) return;
  loading.value = true; error.value = '';
  try { const value = await request('/certificates'); if (live) report.value = value; }
  catch(e) { if (live) error.value = e instanceof Error ? e.message : String(e); }
  finally { if (live) loading.value = false; }
}
onMounted(load); onUnmounted(() => { live = false; });
</script>
<template>
  <section class="panel">
    <header><div><h2>{{ t('Сертификаты') }}</h2><p>{{ t('Последняя проверка') }}: {{ certificateDate(report?.checkedAt) }}</p></div><button :disabled="loading" @click="load">{{ t('Обновить') }}</button></header>
    <p class="footnote">{{ t('Результаты кэшируются на 5 минут. Предупреждения: 30 дней, срочно: 14 дней, критично: 7 дней или истёкший срок.') }}</p>
    <p class="footnote">{{ t('Talos API автоматически обновляет короткоживущие сертификаты: предупреждение за 1 час, критично за 10 минут. Менее суток само по себе не означает проблему.') }}</p>
    <p v-if="error" class="notice error" role="alert">{{ error }} · {{ t('Состояние не подтверждено. Повторите проверку.') }}</p>
    <div v-if="loading && !report" class="loading-state">{{ t('Проверка сертификатов…') }}</div>
    <template v-else>
      <div class="summary-strip"><span>{{ t('Состояние') }} <strong>{{ error || !report?.certificates?.length ? t('Неизвестно') : certificateStatus(report.status) }}</strong></span><span v-for="status in (['healthy','warning','critical','unknown'] as const)" :key="status">{{ certificateStatus(status) }} <strong>{{ report?.summary?.[status] ?? '—' }}</strong></span></div>
      <div class="toolbar"><label>{{ t('Состояние') }}<select v-model="filter"><option value="all">{{ t('Все') }}</option><option v-for="status in ['healthy','warning','critical','unknown']" :key="status" :value="status">{{ certificateStatus(status) }}</option></select></label></div>
      <ResourceTable :rows="rows" :empty="t('Нет данных о сертификатах. Действительность не подтверждена.')" :columns="[{key:'name',title:t('Сертификат')},{key:'statusLabel',title:t('Состояние')},{key:'remaining',title:t('Осталось')},{key:'expiry',title:t('Истекает')},{key:'sourceLabel',title:t('Источник')},{key:'endpoint',title:'Endpoint'}]" @select="selected = $event" />
    </template>
  </section>
  <Modal v-if="selected" :title="selected.name" @close="selected = null">
    <p v-if="selected.verification === 'failed' || selected.verification === 'unavailable'" class="notice error">{{ t('TLS-проверка не пройдена. Показанные метаданные не подтверждают доверие к endpoint.') }}</p>
    <dl class="detail-grid"><dt>{{ t('Состояние') }}</dt><dd>{{ certificateStatus(selected.status) }}</dd><dt>{{ t('Источник') }}</dt><dd>{{ certificateText(selected.source) }}</dd><dt>Endpoint</dt><dd>{{ selected.endpoint || '—' }}</dd><dt>{{ t('Нода') }}</dt><dd>{{ selected.node || '—' }}</dd><dt>{{ t('Действует с') }}</dt><dd>{{ certificateDate(selected.notBefore) }}</dd><dt>{{ t('Истекает') }}</dt><dd>{{ certificateDate(selected.notAfter) }}</dd><dt>{{ t('Проверка доверия') }}</dt><dd>{{ selected.verification === 'embedded' ? t('Сертификат из конфигурации') : selected.verified ? t('TLS подтверждён') : t('Не подтверждено') }}</dd></dl>
    <p class="inspection-copy">{{ certificateText(selected.reason) }}</p><p v-if="selected.error" class="notice error">{{ selected.error }}</p><h3>{{ t('Обновление сертификата') }}</h3><p class="inspection-copy">{{ certificateText(selected.renewalGuidance) }}</p>
  </Modal>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { request } from './client';
import { t } from './i18n';
import ResourceTable from './ResourceTable.vue';
import { recoveryState, recoveryAutomationPaused, recoveryAutomationResumeRecorded, checkRecoveryStatus } from './recovery';

type Record = {
  id: string; kind: string; outcome: string; observedAt: string;
  backupCreatedAt?: string; uploadedAt?: string; checksumVerifiedAt?: string;
  decryptVerifiedAt?: string; schemaVerifiedAt?: string; restoreTestedAt?: string;
  schemaVersion?: number; applicationVersion?: string; target?: string;
  sizeBytes?: number; durationSeconds?: number; error?: string;
};
type Protection = {
  historyConfigured: boolean; records: Record[];
  lastBackupAt?: string; lastBackupUploadedAt?: string; recoveryPointSeconds?: number;
  lastRestoreTestedAt?: string; lastRestoreTestedForBackupCreatedAt?: string;
  lastVerificationAt?: string; unresolvedObservation: boolean;
};

const data = ref<Protection | null>(null), loading = ref(false), error = ref('');
const reason = ref(''), resuming = ref(false), resumeError = ref(''), resumeResult = ref('');
let live = true;

// Absent evidence is shown as unknown, never as a healthy value.
const unknown = () => t('Неизвестно');
function moment(value?: string) {
  if (!value) return unknown();
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? unknown() : parsed.toLocaleString();
}
function check(value?: string) { return value ? moment(value) : '—'; }
function age(seconds?: number) {
  if (typeof seconds !== 'number' || seconds < 0) return unknown();
  if (seconds < 3600) return t('{0} мин.', [Math.floor(seconds / 60)]);
  if (seconds < 172800) return t('{0} ч.', [Math.floor(seconds / 3600)]);
  return t('{0} дн.', [Math.floor(seconds / 86400)]);
}
const outcomeLabel = (outcome: string) => outcome === 'succeeded' ? t('Подтверждено') : outcome === 'failed' ? t('Ошибка') : t('Не доказано');
const kindLabel = (kind: string) => kind === 'backup' ? t('Резервная копия') : kind === 'drill' ? t('Проверочное восстановление') : t('Восстановление');

const rows = computed(() => (data.value?.records || []).map(r => ({
  ...r,
  observed: moment(r.observedAt),
  kindLabel: kindLabel(r.kind),
  outcomeLabel: outcomeLabel(r.outcome),
  created: check(r.backupCreatedAt),
  uploaded: check(r.uploadedAt),
  checksum: check(r.checksumVerifiedAt),
  decrypt: check(r.decryptVerifiedAt),
  schema: check(r.schemaVerifiedAt),
  drill: check(r.restoreTestedAt),
  target: r.target || '—',
})));

const canResume = computed(() => recoveryState.value === 'normal' && recoveryAutomationPaused.value && !recoveryAutomationResumeRecorded.value);

async function load() {
  if (loading.value) return;
  loading.value = true; error.value = '';
  try {
    const value = await request<Protection>('/recovery/protection', {}, 'global');
    if (live) data.value = value;
  } catch (e) { if (live) error.value = e instanceof Error ? e.message : String(e); }
  finally { if (live) loading.value = false; }
}
async function resume() {
  if (resuming.value) return;
  resuming.value = true; resumeError.value = ''; resumeResult.value = '';
  try {
    const value = await request<{ message: string }>('/recovery/automation/resume', { method: 'POST', body: JSON.stringify({ reason: reason.value.trim() }) }, 'global');
    if (!live) return;
    resumeResult.value = value?.message || t('Решение записано. Автоматизация запустится после перезапуска TalosDeck.');
    reason.value = '';
    await checkRecoveryStatus(true);
  } catch (e) { if (live) resumeError.value = e instanceof Error ? e.message : String(e); }
  finally { if (live) resuming.value = false; }
}
onMounted(() => { load(); checkRecoveryStatus(true); });
onUnmounted(() => { live = false; });
</script>
<template>
  <section class="panel">
    <header>
      <div>
        <h2>{{ t('Восстановление TalosDeck') }}</h2>
        <p>{{ t('Защита самой панели управления. Это не резервные копии etcd подключённых кластеров.') }}</p>
      </div>
      <button :disabled="loading" @click="load">{{ t('Обновить') }}</button>
    </header>

    <p v-if="error" class="notice error" role="alert">{{ error }} · {{ t('Состояние защиты не подтверждено.') }}</p>
    <p v-else-if="data && !data.historyConfigured" class="notice" role="status">{{ t('История проверок не настроена: команды recovery запускаются без --history. Наличие или отсутствие копий отсюда не подтверждается.') }}</p>
    <p v-else-if="data && !data.records.length" class="notice" role="status">{{ t('Наблюдений пока нет. Это не означает, что резервная копия существует или отсутствует.') }}</p>
    <p v-if="data?.unresolvedObservation" class="notice error" role="alert">{{ t('Есть незавершённые наблюдения: результат переноса не доказан. Проверьте цель вручную, не считая копию пригодной.') }}</p>

    <div class="summary-strip">
      <span>{{ t('Последняя доказанная копия') }} <strong>{{ moment(data?.lastBackupAt) }}</strong></span>
      <span>{{ t('Фактический RPO') }} <strong>{{ age(data?.recoveryPointSeconds) }}</strong></span>
      <span>{{ t('Выгружена off-host') }} <strong>{{ moment(data?.lastBackupUploadedAt) }}</strong></span>
      <span>{{ t('Проверка целостности') }} <strong>{{ moment(data?.lastVerificationAt) }}</strong></span>
      <span>{{ t('Проверочное восстановление') }} <strong>{{ moment(data?.lastRestoreTestedAt) }}</strong></span>
    </div>
    <p class="footnote">{{ t('RPO считается только по времени создания резервной копии. Проверочное восстановление подтверждает пригодность копии, но не создаёт новую копию, не сдвигает RPO и не продлевает её срок хранения.') }}</p>
    <p v-if="data?.lastRestoreTestedForBackupCreatedAt" class="footnote">{{ t('Последняя проверка относится к копии от {0}.', [moment(data.lastRestoreTestedForBackupCreatedAt)]) }}</p>
    <p class="footnote">{{ t('Независимость домена отказа цели и отдельное хранение мастер-ключа проверяет оператор; панель этого не доказывает. Резервная копия без соответствующего ключа не восстанавливает credentials.') }}</p>

    <ResourceTable
      :rows="rows"
      :empty="t('Нет записей о создании и проверках копий.')"
      :columns="[
        {key:'observed',title:t('Наблюдение')},
        {key:'kindLabel',title:t('Операция')},
        {key:'outcomeLabel',title:t('Итог')},
        {key:'created',title:t('Создана')},
        {key:'uploaded',title:t('Выгружена')},
        {key:'checksum',title:t('Контрольная сумма')},
        {key:'decrypt',title:t('Расшифровка')},
        {key:'schema',title:t('Схема')},
        {key:'drill',title:t('Проверочное восстановление')},
        {key:'target',title:t('Цель')},
      ]" />
  </section>

  <section class="panel">
    <header><div><h2>{{ t('Режим работы и автоматизация') }}</h2><p>{{ t('Расписания, сборщики и фоновая доставка уведомлений после восстановления.') }}</p></div></header>
    <p v-if="recoveryState === 'safe'" class="notice error" role="status">{{ t('Безопасный режим: изменения инфраструктуры запрещены. Сначала выполните офлайн-активацию и разберите прерванные задания.') }}</p>
    <p v-else-if="recoveryAutomationResumeRecorded" class="notice" role="status">{{ t('Возобновление записано и ожидает перезапуска. Автоматизация в этом процессе остаётся приостановленной.') }}</p>
    <p v-else-if="recoveryAutomationPaused" class="notice" role="status">{{ t('Автоматизация приостановлена. Ручные разрешённые действия доступны; прерванные задания сами не продолжаются.') }}</p>
    <p v-else class="notice" role="status">{{ t('Обычный режим: автоматизация активна.') }}</p>

    <form v-if="canResume" class="settings-form" @submit.prevent="resume">
      <label>{{ t('Основание возобновления') }}
        <input v-model="reason" :placeholder="t('Прерванные задания разобраны, прежний экземпляр fenced')" maxlength="512" required />
      </label>
      <p class="footnote">{{ t('Решение записывается в аудит и применяется при следующем запуске TalosDeck: расписания не включаются в работающем процессе. Прерванные задания не продолжаются автоматически.') }}</p>
      <button type="submit" class="primary" :disabled="resuming || !reason.trim()">{{ resuming ? t('Запись…') : t('Записать возобновление автоматизации') }}</button>
    </form>
    <p v-if="resumeResult" class="notice" role="status">{{ resumeResult }}</p>
    <p v-if="resumeError" class="notice error" role="alert">{{ resumeError }}</p>
  </section>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue';
import ConsoleApp from "./console/ConsoleApp.vue";
import { recoveryState, checkRecoveryStatus } from './console/recovery';
import { t, locale, setLocale } from './console/i18n';
let recoveryPoll: ReturnType<typeof setInterval> | undefined;
onMounted(() => { void checkRecoveryStatus(); recoveryPoll = setInterval(() => { void checkRecoveryStatus(true); }, 30000); });
onUnmounted(() => clearInterval(recoveryPoll));
</script>
<template>
  <div v-if="recoveryState === 'checking' || recoveryState === 'unavailable'" class="recovery-startup" role="status">
    <h1>TalosDeck</h1>
    <p>{{ recoveryState === 'checking' ? t('Проверяем режим восстановления…') : t('Не удалось проверить режим восстановления. Управление недоступно до проверки состояния сервера.') }}</p>
    <button v-if="recoveryState === 'unavailable'" @click="checkRecoveryStatus()">{{ t('Повторить') }}</button>
    <select :value="locale" aria-label="Language / Язык" @change="setLocale(($event.target as HTMLSelectElement).value)"><option value="ru">Русский</option><option value="en">English</option></select>
  </div>
  <ConsoleApp v-else />
</template>
<style scoped>
.recovery-startup { max-width: 640px; margin: 12vh auto; padding: 24px; }
.recovery-startup button, .recovery-startup select { margin: 12px 12px 0 0; padding: 8px 12px; }
</style>

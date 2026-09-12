import { ref } from 'vue';
export const recoveryAutomationPaused = ref(false);
export const recoveryState = ref<'checking' | 'normal' | 'safe' | 'unavailable'>('checking');
export async function checkRecoveryStatus(background = false) {
  if (!background) recoveryState.value = 'checking';
  try {
    const response = await fetch('/api/recovery/status', { cache: 'no-store', signal: AbortSignal.timeout(10000) });
    if (!response.ok) throw new Error('Recovery status unavailable');
    const data = await response.json();
    if (typeof data.safeMode !== 'boolean' || typeof data.requiresReview !== 'boolean') throw new Error('Invalid recovery status');
    recoveryAutomationPaused.value = data.automationPaused === true;
    recoveryState.value = data.safeMode || data.requiresReview ? 'safe' : 'normal';
  } catch { recoveryState.value = 'unavailable'; }
}

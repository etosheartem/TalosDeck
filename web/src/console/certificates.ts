import { t, locale } from './i18n';
export interface Certificate {
  id: string; name: string; source: string; endpoint?: string; node?: string;
  notBefore?: string; notAfter?: string; daysRemaining?: number;
  status: 'healthy' | 'warning' | 'critical' | 'unknown'; reason: string;
  verified: boolean; verification: 'embedded' | 'verified' | 'failed' | 'unavailable';
  error?: string; renewalGuidance: string;
}
export interface CertificateReport {
  checkedAt: string; certificates: Certificate[]; status: Certificate['status'];
  summary: { healthy: number; warning: number; critical: number; unknown: number };
}
export function certificateStatus(status?: string) {
  return t(({healthy:'Действителен',warning:'Скоро истекает',critical:'Требует внимания',unknown:'Неизвестно'} as Record<string,string>)[status || 'unknown'] || 'Неизвестно');
}
export function certificateDate(value?: string) {
  if (!value || !Number.isFinite(Date.parse(value))) return '—';
  return new Date(value).toLocaleString(locale.value === 'ru' ? 'ru-RU' : 'en-GB', {dateStyle:'medium',timeStyle:'short'});
}

const certificateLabels: Record<string, string> = {
  "Issue replacement Talos client credentials outside TalosDeck with the cluster owner before expiry. This monitor cannot replace stored credentials; consult the deployment documentation.": "До истечения срока выпустите новые Talos credentials вне TalosDeck вместе с владельцем кластера. Этот монитор не заменяет сохранённые credentials; обратитесь к документации по развёртыванию.",
  "Obtain replacement kubeconfig credentials outside TalosDeck from the Kubernetes cluster owner. This monitor cannot replace stored credentials; consult the deployment documentation.": "Получите новые kubeconfig credentials вне TalosDeck у владельца Kubernetes-кластера. Этот монитор не заменяет сохранённые credentials; обратитесь к документации по развёртыванию.",
  "Plan Talos CA rotation with the cluster owner using the supported Talos procedure. Renewing a client certificate does not renew its CA; this monitor cannot rotate CAs or replace stored credentials.": "Запланируйте ротацию Talos CA вместе с владельцем кластера по поддерживаемой процедуре Talos. Обновление клиентского сертификата не обновляет CA. Этот монитор не выполняет ротацию CA и не заменяет сохранённые credentials.",
  "Plan Kubernetes CA rotation with the cluster owner using the supported Talos procedure. Reissuing kubeconfig credentials does not renew the cluster CA; this monitor cannot rotate CAs or replace stored credentials.": "Запланируйте ротацию Kubernetes CA вместе с владельцем кластера по поддерживаемой процедуре Talos. Перевыпуск kubeconfig credentials не обновляет CA кластера. Этот монитор не выполняет ротацию CA и не заменяет сохранённые credentials.",
  'Talos API server certificates are automatically rotated and short-lived. The 1-hour warning and 10-minute critical thresholds are TalosDeck monitoring policy. Check node time and rotation health; do not manually replace a healthy rotating certificate or disable TLS verification.':'Сертификаты Talos API короткоживущие и обновляются автоматически. Пороги 1 час и 10 минут — политика мониторинга TalosDeck. Проверьте время ноды и работу ротации; не заменяйте исправный сертификат вручную и не отключайте TLS.',
  'talosconfig':'talosconfig', 'kubeconfig':'kubeconfig', 'talos-server':'Talos API', 'kubernetes-server':'Kubernetes API',
  'valid':'Срок действия в норме', 'expires-within-30-days':'Истекает в течение 30 дней', 'expires-within-14-days':'Срочно: истекает в течение 14 дней', 'expires-within-7-days':'Критично: истекает в течение 7 дней',
  'expires-within-1-hour':'Talos API: истекает в течение часа', 'expires-within-10-minutes':'Talos API: истекает в течение 10 минут',
  'expired':'Сертификат истёк', 'not-yet-valid':'Сертификат ещё не действует', 'unavailable':'Проверка недоступна', 'verification-failed':'Проверка доверия не пройдена',
  "Inspect the node time, server certificate chain and SANs; renew through the cluster's Talos/Kubernetes certificate lifecycle. Do not disable TLS verification.":'Проверьте время ноды, цепочку сертификатов и SAN. Обновите сертификат средствами Talos/Kubernetes. Не отключайте проверку TLS.',
};
export const certificateText = (value: string) => t(certificateLabels[value] || value);

import { ref } from 'vue';
import { t } from './i18n';
export const jobFocus = ref('');
export const actionFeedback = ref<{id:string; clusterId:string; global:boolean; kind:string; status:string; node:string} | null>(null);
const names: Record<string,string> = {"talos-upgrade": "Обновление Talos", "kubernetes-upgrade": "Обновление Kubernetes", "rolling-reboot": "Последовательная перезагрузка", "config-apply": "Изменение конфигурации", "config-restore": "Восстановление конфигурации", "cluster-create": "Создание кластера", "worker-create": "Создание worker", "worker-replace": "Замена worker", "worker-delete": "Удаление машины", "machine-cleanup": "Очистка ресурсов", "backup-create": "Создание резервной копии", "backup-restore": "Восстановление резервной копии", "diagnostics": "Диагностика"};
export const actionName = (kind:string) => t(names[kind] || kind);
export function actionState(status:string) {
 return t(({queued:'В очереди',running:'Выполняется',succeeded:'Завершено',failed:'Ошибка — проверьте журнал',interrupted:'Прервано — результат требует проверки',unknown:'Результат неизвестен',requires_review:'Требуется проверка',stopped:'Остановлено — проверьте частичный результат'} as Record<string,string>)[status.toLowerCase()] || 'Результат неизвестен');
}
// Observe only jobs accepted in this session; historical lists never create success notifications.
export function observeJobResponse(data:any, accepted:boolean, clusterId:string, global:boolean) {
 const entries=Array.isArray(data)?data:[data?.job || data];
 for(const job of entries) {
  if(!job?.id || !job.request?.kind || !job.status) continue;
  const previous=actionFeedback.value;
  if(!accepted && (!previous || previous.id!==job.id || previous.clusterId!==clusterId || previous.global!==global)) continue;
  actionFeedback.value={id:job.id,clusterId,global,kind:job.request.kind,status:job.reconciliationOutcome==='UNKNOWN'?'unknown':job.reconciliationOutcome==='requires_review'?'requires_review':job.status,node:job.request.node || ''};
 }
}

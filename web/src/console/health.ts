import {t} from './i18n';
export interface HealthCheck {id:string;category:string;resource?:string;node?:string;state:string;reason:string;title:string;details:string;suggestedAction:string;observedAt?:string}
export interface HealthCategory {id:string;name:string;weight:number;score:number|null;observedScore:number|null;coverage:number;status:string;deductions:{checkId:string;points:number;reason:string}[];checks:HealthCheck[]}
export interface HealthReport {policyVersion:number;snapshotId:string;checkedAt:string;stale:boolean;status:string;score:number|null;observedScore:number|null;coverage:number;categories:HealthCategory[];findings:HealthCheck[]}
export function healthLabel(value:string){return ({'not-applicable':t('Не применимо'),healthy:t('Исправен'),warning:t('Предупреждение'),critical:t('Критично'),unknown:t('Неизвестно'),missing:t('Нет данных'),stale:t('Данные устарели'),partial:t('Недостаточно данных'),'control-plane':t('Control plane'),etcd:'etcd',nodes:t('Ноды'),storage:t('Хранилище'),networking:t('Сеть'),workloads:t('Рабочие нагрузки'),backups:t('Резервные копии'),certificates:t('Сертификаты')})[value]||value;}
export function healthExpired(report:HealthReport|null,now=Date.now()){return !report||!report.snapshotId||report.stale||!Number.isFinite(Date.parse(report.checkedAt))||Date.parse(report.checkedAt)>now||now-Date.parse(report.checkedAt)>600000;}

export const healthNumber=(value:number|null|undefined)=>typeof value==='number'&&Number.isFinite(value)?String(Math.round(value*10)/10):'—';

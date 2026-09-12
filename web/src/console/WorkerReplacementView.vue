<script setup lang="ts">
import {computed,onMounted,onUnmounted,ref,watch} from 'vue';
import {request,globalRequest} from './client';
import {isAdmin} from './permissions';
import {t,locale} from './i18n';
import ImagePicker,{type ImageProfile} from './ImagePicker.vue';
import ResourceTable from './ResourceTable.vue';
const emit=defineEmits<{submitted:[];jobs:[]}>();
const machines=ref<any[]>([]),providers=ref<any[]>([]),history=ref<any[]>([]);
const machineId=ref(''),mode=ref('reachable'),imageMode=ref('factory');
const busy=ref(false),error=ref(''),plan=ref<any>(null),confirmation=ref(''),acknowledged=ref(false);
const spec=ref({kind:'worker-create',name:'',providerId:'',talosVersion:'',kubernetesVersion:'',installerImage:'',schematicId:'',architecture:'amd64',platform:'metal',isoStorage:'',machines:[{name:'worker-replacement',role:'worker',cores:2,memoryMB:2048,diskGB:40,storage:'',bridge:'',iso:'',vlan:0,networkMode:'dhcp',address:'',gateway:'',nameservers:[] as string[]}]});
const nameservers=ref('');
let live=true;let generation=0;
const selected=computed(()=>machines.value.find(m=>m.id===machineId.value));
const canSubmit=computed(()=>isAdmin.value&&!busy.value&&plan.value&&plan.value.impact?.unknown===false&&!!plan.value.impactHash&&confirmation.value===plan.value.nodeName&&acknowledged.value);
function invalidate(){generation++;plan.value=null;confirmation.value='';acknowledged.value=false;}
watch([machineId,mode,imageMode,nameservers,spec],invalidate,{deep:true,flush:'sync'});
watch(imageMode,()=>{spec.value.machines[0]!.iso='';});
watch(()=>spec.value.machines[0]!.networkMode,value=>{if(value==='dhcp'){spec.value.machines[0]!.address='';spec.value.machines[0]!.gateway='';nameservers.value='';}});
watch(machineId,()=>{if(selected.value)spec.value.providerId=selected.value.providerId;});
function chooseImage(profile:ImageProfile|null){spec.value.schematicId=profile?.id||'';spec.value.talosVersion=profile?.version||'';spec.value.installerImage=profile?.installerImage||'';}
async function run(fn:()=>Promise<void>){if(busy.value)return;busy.value=true;error.value='';try{await fn();}catch(e){if(live)error.value=String(e);}finally{if(live)busy.value=false;}}
async function load(){const [owned,ps,plans,cluster]=await Promise.all([request('/machines'),globalRequest('/providers'),request('/replacements'),request('/cluster')]);if(!live)return;machines.value=(Array.isArray(owned)?owned:owned.machines||[]).filter((m:any)=>m.role==='worker'&&m.status==='ready');providers.value=Array.isArray(ps)?ps:ps.providers||[];history.value=Array.isArray(plans)?plans:plans.plans||[];spec.value.name=cluster.name||'';spec.value.kubernetesVersion=cluster.kubernetesVersion||'';machineId.value=machines.value[0]?.id||'';}
function installPlan(result:any){plan.value=result;confirmation.value='';acknowledged.value=false;}
async function preview(){if(!isAdmin.value||!selected.value)return;await run(async()=>{const current=generation;const replacement=JSON.parse(JSON.stringify(spec.value));replacement.machines[0].nameservers=nameservers.value.split(',').map(v=>v.trim()).filter(Boolean);if(imageMode.value==='manual'){delete replacement.schematicId;delete replacement.architecture;delete replacement.platform;delete replacement.isoStorage;}const result=await request('/replacements/plan',{method:'POST',body:JSON.stringify({machineId:machineId.value,mode:mode.value,replacement})});if(live&&current===generation)installPlan(result);});}
async function submit(){if(!canSubmit.value)return;const approval=plan.value;await run(async()=>{await request('/replacements',{method:'POST',body:JSON.stringify({planId:approval.id,confirmedName:confirmation.value,impactHash:approval.impactHash,acknowledgeStorageImpact:acknowledged.value})});if(live)emit('submitted');});}
async function refreshHistory(){await run(async()=>{const value=await request('/replacements');if(live)history.value=Array.isArray(value)?value:value.plans||[];});}
async function resumePlan(id:string){if(!isAdmin.value)return;await run(async()=>{const current=generation;const result=await request(`/replacements/${encodeURIComponent(id)}/resume-plan`,{method:'POST'});if(live&&current===generation)installPlan(result);});}
const date=(s:string)=>new Date(s).toLocaleString(locale.value==='ru'?'ru-RU':'en-US');
onMounted(()=>run(load));onUnmounted(()=>{live=false;generation++;});
</script>
<template>
 <div class="module-stack worker-replacement">
  <p class="notice warning">{{t('Замена безвозвратно удалит старую VM после проверки принадлежности. Выключение VM не считается fencing. Данные локальных дисков не переносятся.')}}</p>
  <p v-if="error" class="notice error" role="alert">{{error}}</p>
  <form class="settings-form panel" @submit.prevent="preview">
   <fieldset :disabled="busy||!isAdmin">
    <h2>{{t('Заменить worker')}}</h2>
    <p>{{t('Доступны только зарегистрированные worker-машины TalosDeck. Control plane здесь заменить нельзя.')}}</p>
    <div class="form-grid">
     <label>{{t('Старая машина')}}<select :aria-label="t('Старая машина')" v-model="machineId" required><option value="" disabled>{{t('Выберите машину')}}</option><option v-for="m in machines" :key="m.id" :value="m.id">{{m.name}} · {{m.providerNode}} / {{m.vmid}}</option></select></label>
     <label>{{t('Режим замены')}}<select :aria-label="t('Режим замены')" v-model="mode"><option value="reachable">{{t('Нода доступна')}}</option><option value="dead">{{t('Нода недоступна')}}</option></select></label>
    </div>
    <p class="footnote">{{mode==='reachable'?t('Drain → новая нода Ready → удалить старую VM → удалить старую запись Node.'):t('Проверить влияние → удалить старую VM → удалить старую запись Node → создать замену. Недоступность API провайдера блокирует процесс.')}}</p>
    <h3>{{t('Новая машина')}}</h3>
    <label>{{t('Провайдер')}}<select v-model="spec.providerId" required><option v-for="p in providers.filter(p=>p.id===selected?.providerId)" :key="p.id" :value="p.id">{{p.name}}</option></select></label>
    <label>{{t('Источник образа')}}<select :aria-label="t('Источник образа')" v-model="imageMode"><option value="factory">Image Factory</option><option value="manual">{{t('Ручной ISO и installer')}}</option></select></label>
    <ImagePicker v-if="imageMode==='factory'" :require-qemu="true" @selected="chooseImage" />
    <label v-if="imageMode==='factory'">{{t('Хранилище ISO в Proxmox')}}<input v-model="spec.isoStorage" placeholder="local" required /></label>
    <div v-else class="form-grid">
     <label>Talos<input v-model="spec.talosVersion" placeholder="1.x.y" required /></label>
     <label>Installer<input v-model="spec.installerImage" placeholder="factory.talos.dev/metal-installer/…:v1.x.y" required /></label>
     <label>ISO<input v-model="spec.machines[0]!.iso" placeholder="data:iso/talos.iso" required /></label>
    </div>
    <div class="form-grid">
     <label>{{t('Имя новой ноды')}}<input v-model="spec.machines[0]!.name" required /></label>
     <label>CPU<input v-model.number="spec.machines[0]!.cores" type="number" min="1" max="128" required /></label>
     <label>RAM (MiB)<input v-model.number="spec.machines[0]!.memoryMB" type="number" min="1024" required /></label>
     <label>{{t('Диск')}} (GiB)<input v-model.number="spec.machines[0]!.diskGB" type="number" min="8" required /></label>
     <label>{{t('Хранилище')}}<input v-model="spec.machines[0]!.storage" placeholder="local-lvm" /></label>
     <label>Bridge<input v-model="spec.machines[0]!.bridge" placeholder="vmbr0" /></label>
     <label>VLAN<input v-model.number="spec.machines[0]!.vlan" type="number" min="0" max="4094" /></label>
     <label>{{t('Сеть')}}<select v-model="spec.machines[0]!.networkMode"><option value="dhcp">DHCP</option><option value="static">Static IPv4</option></select></label>
     <template v-if="spec.machines[0]!.networkMode==='static'"><label>IPv4 / CIDR<input v-model="spec.machines[0]!.address" required /></label><label>{{t('Шлюз')}}<input v-model="spec.machines[0]!.gateway" required /></label><label>DNS<input v-model="nameservers" placeholder="1.1.1.1, 9.9.9.9" required /></label></template>
    </div>
    <button class="primary" :disabled="!selected||busy||(imageMode==='factory'&&!spec.schematicId)">{{t('Проверить план замены')}}</button>
   </fieldset>
  </form>
  <section v-if="plan" class="panel replacement-plan">
   <header><h2>{{t('План замены')}} · {{plan.nodeName}}</h2><code>{{plan.id}}</code></header>
   <p>{{t('Идентичность старой ноды')}}: <code>{{plan.nodeUID}}</code> · {{plan.mode}}</p>
   <p>{{t('Новая машина')}}: <strong>{{plan.replacementPlan?.spec?.machines?.[0]?.name}}</strong> · {{plan.replacementPlan?.spec?.providerId}}</p>
   <p v-for="note in plan.safetyNotes||[]" :key="note" class="footnote">{{note}}</p>
   <div v-if="plan.resume" class="notice warning"><p>{{t('Перед подтверждением продолжения откройте журнал заданий, проверьте исходное прерванное задание этой замены и явно подтвердите проверку. Его блокировка не снимается автоматически.')}}</p><button @click="emit('jobs')">{{t('Открыть журнал заданий')}}</button></div>
   <h3>{{t('Влияние на workloads и данные')}}</h3>
   <p v-if="plan.impact?.unknown!==false" class="notice error" role="alert">{{t('Влияние определено не полностью. Запуск заблокирован; сначала восстановите доступ к источникам данных.')}}</p>
   <p v-if="plan.impact?.observedAt" class="footnote">{{t('Проверено')}}: {{date(plan.impact.observedAt)}}</p>
   <ul v-if="plan.impact?.issues?.length"><li v-for="issue in plan.impact.issues" :key="issue">{{issue}}</li></ul>
   <ResourceTable :rows="(plan.impact?.pods||[]).map((p:any)=>({...p,controller:[p.controllerKind,p.controllerName].filter(Boolean).join('/'),risk:p.reasons?.join('; ')||'—'}))" :columns="[{key:'namespace',title:'Namespace'},{key:'name',title:'Pod'},{key:'controller',title:t('Контроллер')},{key:'risk',title:t('Риски')}]" />
   <details v-for="(v,i) in plan.impact?.volumes||[]" :key="i" class="volume-impact">
    <summary>{{v.namespace}} / {{v.pod}} · {{v.volume}} · {{v.state}}</summary>
    <dl><dt>PVC / PV</dt><dd>{{v.pvc||'—'}} / {{v.pv||'—'}}</dd><dt>PVC UID / PV UID</dt><dd>{{v.pvcUID||'—'}} / {{v.pvUID||'—'}}</dd><dt>StorageClass / Provisioner</dt><dd>{{v.storageClass||'—'}} / {{v.provisioner||'—'}}</dd><dt>Reclaim policy / Access modes</dt><dd>{{v.reclaimPolicy||'—'}} / {{v.accessModes?.join(', ')||'—'}}</dd><dt>CSI driver / Attach required</dt><dd>{{v.csiDriver||'—'}} / {{v.csiAttachRequired??'—'}}</dd><dt>Local path</dt><dd>{{v.localPath||'—'}}</dd><dt>Volume binding</dt><dd>{{v.volumeBindingMode||'—'}}</dd></dl>
    <p v-for="reason in v.reasons||[]" :key="reason" class="notice warning">{{reason}}</p>
    <details v-if="v.nodeAffinity||v.allowedTopologies?.length"><summary>Node affinity / Allowed topologies</summary><pre>{{JSON.stringify({nodeAffinity:v.nodeAffinity,allowedTopologies:v.allowedTopologies},null,2)}}</pre></details>
    <ResourceTable v-if="v.attachments?.length" :rows="v.attachments" :columns="[{key:'name',title:'VolumeAttachment'},{key:'nodeName',title:t('Нода')},{key:'attacher',title:'CSI'},{key:'attached',title:'Attached'},{key:'deleting',title:'Deleting'},{key:'hasAttachError',title:'Attach error'},{key:'hasDetachError',title:'Detach error'}]" />
   </details>
   <p class="footnote">{{t('Новая нода Ready')}}: {{plan.newNodeReady===undefined?'—':plan.newNodeReady?t('Да'):t('Нет')}} · {{t('Старая запись Node удалена')}}: {{plan.oldNodeRemoved===undefined?'—':plan.oldNodeRemoved?t('Да'):t('Нет')}}</p>
   <p v-if="plan.fence" class="footnote">Fencing: {{plan.fence.outcome}} · {{plan.fence.source}} · {{plan.fence.verifiedAt}}</p>
   <p v-if="plan.replacementPlan?.id" class="footnote">{{t('Закреплённый план новой машины')}}: <code>{{plan.replacementPlan?.id}}</code></p>
   <label class="replacement-check"><input v-model="acknowledged" type="checkbox" :disabled="busy||!isAdmin" />{{t('Я проверил влияние на workloads и storage. Подтверждаю безвозвратное удаление старой VM; локальные данные автоматически не восстанавливаются.')}}</label>
   <label>{{t('Введите имя старой машины')}} <code>{{plan.nodeName}}</code><input v-model="confirmation" :disabled="busy||!isAdmin" autocomplete="off" /></label>
   <button class="danger" :disabled="!canSubmit" @click="submit">{{t('Подтвердить замену')}}</button>
  </section>
  <section class="panel"><header><h2>{{t('История замен')}}</h2><button :disabled="busy" @click="refreshHistory">{{t('Обновить')}}</button></header>
   <p v-if="!history.length" class="footnote">{{t('Планов замены пока нет.')}}</p>
   <div v-for="item in history" :key="item.id" class="replacement-history"><strong>{{item.nodeName}}</strong><span>{{item.status}}</span><code>{{item.id}}</code><button v-if="isAdmin&&['interrupted','requires_review','running'].includes(item.status)" :disabled="busy" @click="resumePlan(item.id)">{{t('Сверить и подготовить продолжение')}}</button></div>
  </section>
 </div>
</template>
<style scoped>
.worker-replacement fieldset {border:0;min-width:0;padding:16px}.worker-replacement h3{font-size:14px;margin:16px 0 8px}.replacement-plan>p,.replacement-plan>h3,.replacement-plan>label,.replacement-plan>ul{margin:12px 16px}.replacement-plan>button{margin:0 16px 16px}.replacement-check{display:flex;align-items:flex-start;gap:8px}.replacement-check input{width:auto;flex:none}.volume-impact{margin:12px 16px;border-top:1px solid var(--border);padding-top:12px}.volume-impact summary{cursor:pointer;overflow-wrap:anywhere}.volume-impact dl{display:grid;grid-template-columns:minmax(150px,1fr) minmax(0,2fr);gap:8px;font-size:12px}.volume-impact dd{margin:0;overflow-wrap:anywhere}.volume-impact pre{white-space:pre-wrap;overflow-wrap:anywhere}.replacement-history{display:flex;align-items:center;flex-wrap:wrap;gap:12px;padding:12px 16px}.replacement-history code{overflow-wrap:anywhere}.replacement-plan header{flex-wrap:wrap}.replacement-plan code{overflow-wrap:anywhere}@media(max-width:600px){.volume-impact dl{grid-template-columns:1fr}.volume-impact dd{margin-bottom:8px}}
</style>

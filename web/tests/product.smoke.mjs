import {chromium} from 'playwright';import {createServer} from 'vite';import assert from 'node:assert/strict';import {mkdtemp} from 'node:fs/promises';import {tmpdir} from 'node:os';import {join} from 'node:path';
const artifacts=await mkdtemp(join(tmpdir(),'talos-product-ui-'));
const nativeRequests=[];
const server=await createServer({plugins:[{name:'native-download-fixture',configureServer(server){server.middlewares.use((req,res,next)=>{if(req.url!=='/api/clusters/cluster-a/backups/backup-1/download')return next();nativeRequests.push({url:req.url,method:req.method,authorization:req.headers.authorization});res.writeHead(200,{'Content-Type':'application/octet-stream','Content-Disposition':'attachment; filename="production-etcd.snapshot"'});res.end('native-stream-fixture');});}}],server:{host:'127.0.0.1',port:5176,strictPort:true}});await server.listen();
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
const errors=[];
const node={ip:'10.0.0.1',hostname:'cp-01',role:'controlplane',version:'v1.14.0',ready:true,servicesSummary:{kubelet:'Healthy',containerd:'Healthy',apid:'Healthy'}};
async function contextFor(role='admin'){
 const context=await browser.newContext({acceptDownloads:true,viewport:{width:1440,height:1080}});await context.addInitScript(()=>localStorage.setItem('talosdeck_token','fixture-token'));
 const calls=[],providers=[{id:'pve-a',name:'Proxmox A',kind:'proxmox',baseUrl:'https://pve.example:8006',node:'pve01',configured:true}],targets=[{id:'local',name:'Local',type:'local',configured:true}],globalJobs=[],jobs=[];
 let schedule={enabled:false,intervalHours:6,retention:30,targetId:'local'};
 const backup={id:'backup-1',filename:'production-etcd.snapshot',type:'etcd',size:1024,humanSize:'1 KiB',timestamp:'2026-09-12T00:00:00Z',checksum:'fixture-sha256'};
 const job=(kind,id)=>({id,clusterId:'cluster-a',request:{kind},status:'queued',createdAt:'2026-09-12T00:00:00Z',user:'admin',events:[],step:'queued'});
 await context.route('**/api/**',async route=>{
   const req=route.request(),raw=new URL(req.url()).pathname;if(!raw.startsWith('/api/'))return route.continue();
   const path=raw.replace('/api/clusters/cluster-a','/api');const body=req.postDataJSON();calls.push({raw,path,method:req.method(),body});let result;
   if(path==='/api/auth/providers')result={oidc:{enabled:true,name:'Keycloak',loginUrl:'/api/auth/oidc/login'}};
   else if(path==='/api/auth/me')result={authenticated:true,user:{id:'user-current',username:role,role,provider:'local'}};
   else if(path==='/api/auth/users')result=req.method()==='GET'?{users:[{id:'user-other',username:'teammate',role:'viewer',provider:'local',disabled:false}]}:{id:'user-new'};
   else if(path.startsWith('/api/auth/users/')||path==='/api/auth/password')result={success:true};
   else if(path==='/api/clusters')result={clusters:[{id:'cluster-a',name:'Production',health:'healthy',talosVersion:'v1.14.0',kubernetesVersion:'v1.37.0',endpoints:['10.0.0.1']}]};
   else if(path==='/api/nodes')result=[node];
   else if(path==='/api/cluster')result={name:'Production',talosVersion:'v1.14.0',kubernetesVersion:'v1.37.0',endpoint:'https://k8s.example'};
   else if(path==='/api/cluster/etcd')result={healthy:true,members:[]};
   else if(path==='/api/providers') {if(req.method()==='POST'){const provider={id:'pve-b',name:body.name,kind:body.kind,baseUrl:body.config.baseUrl,node:body.config.node,configured:true};providers.push(provider);result=provider;}else result=providers;}
   else if(path==='/api/provision/plan')result={id:'create-plan',spec:body,safetyNotes:['Fixture plan']};
   else if(path==='/api/provision'&&req.method()==='POST'){result=job('cluster-create','global-job-1');globalJobs.push(result);}
   else if(path==='/api/provision/jobs')result=globalJobs;
   else if(path.startsWith('/api/provision/jobs/'))result=globalJobs[0]||{};
   else if(path==='/api/backups')result=[backup,{...backup,id:'backup-partial',filename:'partial-full.tar.gz',type:'full',partial:true}];
   else if(path==='/api/backups/backup-1/download-ticket'){assert.equal(req.method(),'POST');assert.equal(req.headers().authorization,'Bearer fixture-token');result={ready:true};}
   else if(path==='/api/backups/backup-1/download'){assert.equal(req.headers().authorization,undefined);assert.equal(new URL(req.url()).search,'');return route.continue();}
   else if(path==='/api/backups/targets'){if(role==='viewer')return route.fulfill({status:403,json:{error:'Forbidden'}});if(req.method()==='PUT'){assert(body.name&&!body.targets,'Target upsert must send one target');const{accessKey,secretKey,...safe}=body;targets.push({...safe,configured:true});result=safe;}else result={targets};}
   else if(path==='/api/backups/schedule'){if(req.method()==='PUT')schedule=body;result=schedule;}
   else if(path==='/api/backups/restore-plan')result={id:'restore-plan',backupId:backup.id,warnings:['Existing etcd state will be replaced'],steps:['Verify backup','Restore etcd'],nodes:['10.0.0.1']};
   else if(path==='/api/backups/restore'){assert.equal(body.confirmedCluster,'Production');result=job('backup-restore','restore-job-1');jobs.push(result);}
   else if(path==='/api/backups/create'){result=job('backup-create','backup-job-1');jobs.push(result);}
   else if(path==='/api/diagnostics')result={status:'degraded',checkedAt:'2026-09-12T00:00:00Z',checks:[{id:'disk',title:'Disk pressure',severity:'warning',component:'storage',node:'cp-01',details:'91% used',suggestion:'Expand the disk'}],summary:{critical:0,warning:1,info:0}};
   else if(path==='/api/diagnostics/run'){result=job('diagnostics','diagnose-job-1');jobs.push(result);}
   else if(path==='/api/k8s/pods')result=[{name:'api-0',namespace:'app',status:'Running',nodeName:'cp-01'}];
   else if(path==='/api/k8s/workloads')result={deployments:[{name:'api',namespace:'app',readyReplicas:1,replicas:1}],daemonsets:[],statefulsets:[],jobs:[],cronjobs:[]};
   else if(path==='/api/k8s/pods/app/api-0')result={pod:{name:'api-0'},events:[{reason:'Started',message:'Container started'}],logs:'fixture pod log',volumes:[]};
   else if(path==='/api/k8s/events')result={events:[]};
   else if(path==='/api/k8s/storage')result={persistentVolumes:[],persistentVolumeClaims:[],storageClasses:[]};
   else if(path==='/api/audit')result={events:raw==='/api/audit'?[{action:'provider.create',user:'admin',status:'success',details:{providerId:'pve-a'}},{action:'users.create',user:'admin',status:'success',details:{targetId:'user-other'}}]:[{action:'node.reboot',user:'operator',status:'success',details:{node:'10.0.0.1'}}],total:raw==='/api/audit'?2:1};
   else if(path==='/api/machines')result=[{id:'machine-1',providerId:'pve-a',name:'worker-owned',role:'worker',vmid:120,address:'10.0.0.2',status:'ready',cleanupEligible:false},...(raw==='/api/machines'?[{id:'machine-failed',providerId:'pve-a',name:'cp-failed',role:'controlplane',vmid:121,status:'booting',cleanupEligible:true}]:[])];
   else if(path==='/api/jobs')result=jobs;
   else if(path.startsWith('/api/jobs/'))result=jobs.find(j=>path.includes(j.id))||{};
   else return route.fulfill({status:404,json:{error:`Missing fixture ${path}`}});
   return route.fulfill({json:result});
 });
 const page=await context.newPage();page.on('pageerror',e=>errors.push(String(e)));return{context,page,calls};
}
try{
 const {context,page,calls}=await contextFor();
 await page.goto('http://127.0.0.1:5176');await page.getByRole('heading',{name:'Кластеры',exact:true}).waitFor();
 await page.getByRole('button',{name:'Production',exact:true}).click();await page.getByRole('heading',{name:'Обзор',exact:true}).waitFor();await page.waitForFunction(()=>document.querySelector('.availability-value')?.textContent.includes('1'));
 await page.keyboard.press('Control+k');assert(await page.getByLabel('Найти раздел',{exact:true}).evaluate(el=>el===document.activeElement));
 await page.getByLabel('Найти раздел',{exact:true}).fill('Провайдеры');await page.getByRole('navigation').getByRole('button',{name:'Провайдеры',exact:true}).click();await page.getByLabel('Найти раздел',{exact:true}).fill('');
 await page.getByRole('button',{name:'Добавить провайдера',exact:true}).click();let dialog=page.getByRole('dialog');
 await dialog.getByLabel('Имя',{exact:true}).fill('Provider B');await dialog.getByLabel('Proxmox URL').fill('https://pve-b.example:8006');await dialog.getByLabel('Нода Proxmox').fill('pve02');await dialog.getByLabel('API token').fill('PRIVATE-PROVIDER-TOKEN');await dialog.getByRole('button',{name:'Подключить',exact:true}).click();await dialog.waitFor({state:'hidden'});
 assert(calls.some(c=>c.raw==='/api/providers'&&c.method==='POST'&&c.body.config.apiToken==='PRIVATE-PROVIDER-TOKEN'));
 assert(!(await page.evaluate(()=>JSON.stringify(localStorage))).includes('PRIVATE-PROVIDER'));
 await page.goto('http://127.0.0.1:5176/#global-audit');await page.getByRole('button',{name:'provider.create',exact:true}).waitFor();await page.getByRole('button',{name:'users.create',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'node.reboot',exact:true}).count(),0);await page.goto('http://127.0.0.1:5176/#audit');await page.getByRole('button',{name:'node.reboot',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'provider.create',exact:true}).count(),0);assert(calls.some(c=>c.raw==='/api/audit'));assert(calls.some(c=>c.raw==='/api/clusters/cluster-a/audit'));
 await page.goto('http://127.0.0.1:5176/#fleet-machines');await page.getByRole('button',{name:'worker-owned',exact:true}).click();dialog=page.getByRole('dialog');assert.equal(await dialog.getByRole('button',{name:'Проверить удаление',exact:true}).count(),0);await page.keyboard.press('Escape');await page.getByRole('button',{name:'cp-failed',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByRole('button',{name:'Проверить удаление',exact:true}).click();assert(calls.some(c=>c.raw==='/api/provision/plan'&&c.body.kind==='machine-cleanup'&&c.body.machineId==='machine-failed'));await dialog.getByLabel('Введите имя для подтверждения').fill('cp-failed');await dialog.getByRole('button',{name:'Удалить через задание',exact:true}).click();await dialog.waitFor({state:'hidden'});assert(calls.some(c=>c.raw==='/api/provision'&&c.body.confirmedName==='cp-failed'));
 await page.goto('http://127.0.0.1:5176/#clusters');await page.getByRole('button',{name:'Создать кластер',exact:true}).click();dialog=page.getByRole('dialog');
 await dialog.getByLabel('Имя кластера',{exact:true}).fill('New Cluster');await dialog.getByLabel('Talos',{exact:true}).fill('1.14.0');await dialog.getByLabel('Kubernetes',{exact:true}).fill('1.37.0');await dialog.getByLabel('Installer image').fill('factory.talos.dev/installer/fixture:v1.14.0');await dialog.getByLabel('CNI',{exact:true}).selectOption('flannel');await dialog.getByLabel('Kubernetes Storage',{exact:true}).selectOption('local-path');await dialog.getByRole('button',{name:'Проверить план',exact:true}).click();await dialog.getByLabel('Введите имя кластера').fill('New Cluster');await dialog.getByRole('button',{name:'Создать через задание',exact:true}).click();await dialog.waitFor({state:'hidden'});assert(calls.some(c=>c.raw==='/api/provision/plan'&&c.body.kind==='cluster-create'&&c.body.cni==='flannel'&&c.body.storage==='local-path'));assert(calls.some(c=>c.raw==='/api/provision'&&c.body.confirmedName==='New Cluster'));
 await page.goto('http://127.0.0.1:5176/#backups');await page.getByRole('tab',{name:'Места хранения',exact:true}).click();await page.getByRole('button',{name:'Добавить S3 / MinIO',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByLabel('Имя',{exact:true}).fill('MinIO');await dialog.getByLabel('Endpoint').fill('https://minio.example');await dialog.getByLabel('Bucket',{exact:true}).fill('backups');await dialog.getByLabel('Access key').fill('PRIVATE-S3-ACCESS');await dialog.getByLabel('Secret key').fill('PRIVATE-S3-SECRET');await dialog.getByRole('button',{name:'Сохранить',exact:true}).click();await dialog.waitFor({state:'hidden'});assert(calls.some(c=>c.raw==='/api/clusters/cluster-a/backups/targets'&&c.method==='PUT'&&c.body.secretKey==='PRIVATE-S3-SECRET'));assert(!(await page.evaluate(()=>JSON.stringify(localStorage))).includes('PRIVATE-S3'));
 await page.getByRole('tab',{name:'Расписание',exact:true}).click();await page.getByLabel('Автоматическое резервное копирование').check();await page.getByLabel('Интервал, часы').fill('12');await page.getByRole('button',{name:'Сохранить',exact:true}).click();await page.getByText('Расписание сохранено',{exact:true}).waitFor();
 await page.getByRole('tab',{name:'Резервные копии',exact:true}).click();
 await page.getByRole('button',{name:'production-etcd.snapshot',exact:true}).click();
 const blobOriginal=await page.evaluate(()=>{window.__blobOriginal=Response.prototype.blob;Response.prototype.blob=function(){throw new Error('Backup must not buffer a Blob')};return true;});
 const nativeDownload=page.waitForEvent('download');await page.getByRole('dialog').getByRole('button',{name:'Скачать',exact:true}).click();
 const downloaded=await nativeDownload;assert.equal(downloaded.suggestedFilename(),'production-etcd.snapshot');assert.equal(await downloaded.failure(),null);
 assert(calls.some(c=>c.raw==='/api/clusters/cluster-a/backups/backup-1/download-ticket'&&c.method==='POST'));
 assert(nativeRequests.some(c=>c.url==='/api/clusters/cluster-a/backups/backup-1/download'&&c.method==='GET'&&c.authorization===undefined));
 await page.evaluate(()=>{Response.prototype.blob=window.__blobOriginal;delete window.__blobOriginal;});await page.keyboard.press('Escape');
 await page.getByRole('tab',{name:'Резервные копии',exact:true}).click();await page.getByRole('cell',{name:'Частичная копия',exact:true}).waitFor();await page.getByRole('button',{name:'partial-full.tar.gz',exact:true}).click();await page.getByRole('dialog').getByText('Часть конфигураций машин отсутствует',{exact:false}).waitFor();await page.keyboard.press('Escape');await page.getByRole('button',{name:'production-etcd.snapshot',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByRole('button',{name:'Проверить восстановление',exact:true}).click();await dialog.getByRole('heading',{name:'План восстановления',exact:true}).waitFor();assert(!calls.some(c=>c.path==='/api/backups/restore'));await dialog.getByLabel('Введите имя кластера').fill('wrong');assert(await dialog.getByRole('button',{name:'Восстановить через задание'}).isDisabled());await dialog.getByLabel('Введите имя кластера').fill('Production');await dialog.getByRole('button',{name:'Восстановить через задание'}).click();await page.getByRole('heading',{name:'Задания',exact:true}).waitFor();
 await page.goto('http://127.0.0.1:5176/#diagnostics');await page.getByRole('button',{name:'Disk pressure',exact:true}).click();await page.getByText('Expand the disk',{exact:true}).waitFor();await page.keyboard.press('Escape');await page.getByRole('button',{name:'Проверить кластер',exact:true}).click();await page.getByRole('heading',{name:'Задания',exact:true}).waitFor();assert(calls.some(c=>c.path==='/api/diagnostics/run'&&c.method==='POST'));
 await page.goto('http://127.0.0.1:5176/#workloads');await page.getByRole('button',{name:'api-0',exact:true}).click();await page.getByText('fixture pod log',{exact:true}).waitFor();await page.keyboard.press('Escape');
 await page.goto('http://127.0.0.1:5176/#users');await page.getByRole('button',{name:'Добавить пользователя',exact:true}).click();dialog=page.getByRole('dialog');await dialog.getByLabel('Пользователь',{exact:true}).fill('new-operator');await dialog.getByLabel('Пароль',{exact:true}).fill('fixture-password-123');await dialog.getByLabel('Роль',{exact:true}).selectOption('operator');await dialog.getByRole('button',{name:'Создать',exact:true}).click();await dialog.waitFor({state:'hidden'});assert(calls.some(c=>c.raw==='/api/auth/users'&&c.method==='POST'&&c.body.role==='operator'));await page.getByRole('button',{name:'teammate',exact:true}).click();await page.getByRole('button',{name:'Отозвать сеансы',exact:true}).click();await page.getByRole('dialog').getByText('Сеансы пользователя отозваны',{exact:true}).waitFor();await page.keyboard.press('Escape');
 for(const section of ['clusters','providers','users','diagnostics','backups']){await page.goto(`http://127.0.0.1:5176/#${section}`);await page.waitForTimeout(100);await page.screenshot({path:`${artifacts}/${section}.png`,fullPage:true});}
 await context.close();
 for(const role of ['viewer','operator']){
  const {context,page,calls}=await contextFor(role);await page.goto('http://127.0.0.1:5176/#global-audit');await page.getByRole('button',{name:'users.create',exact:true}).waitFor();await page.goto('http://127.0.0.1:5176/#backups');await page.getByRole('button',{name:'production-etcd.snapshot',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'Создать копию',exact:true}).count(),role==='operator'?1:0);if(role==='viewer')assert(!calls.some(c=>c.path==='/api/backups/targets'));
  await page.goto('http://127.0.0.1:5176/#config');await page.getByRole('heading',{name:'Недостаточно прав',exact:true}).waitFor();assert.equal(await page.getByLabel('YAML patch',{exact:true}).count(),0);assert(!calls.some(c=>c.path.endsWith('/config')));
  await page.goto('http://127.0.0.1:5176/#users');await page.getByRole('heading',{name:'Моя учётная запись',exact:true}).waitFor();assert.equal(await page.getByRole('button',{name:'Добавить пользователя',exact:true}).count(),0);assert(!calls.some(c=>c.path==='/api/auth/users'));
  await page.goto('http://127.0.0.1:5176/#updates');if(role==='operator')await page.getByLabel('Целевая версия').waitFor();else await page.getByRole('heading',{name:'Недостаточно прав',exact:true}).waitFor();await context.close();
 }
 assert.deepEqual(errors,[]);console.log('PASS: P1 provider/global provision, S3 credentials, schedule, confirmed restore, diagnosis jobs, pod inspector, users/revocation, viewer/operator permissions, global-cluster scopes.');console.log('Screenshots:',artifacts);
}finally{await browser.close();await server.close();}

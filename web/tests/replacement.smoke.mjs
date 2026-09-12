import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import {createServer} from 'vite';
const server=await createServer({server:{host:'127.0.0.1',port:5179,strictPort:true}});await server.listen();
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
try{
 const context=await browser.newContext();let unknown=true,submitted=0,resumed=0;
 let captured;
 const plan=()=>({id:'plan',nodeName:'worker-old',nodeUID:'uid-old',mode:'dead',replacementPlan:{id:'child-plan',spec:captured?.replacement||{machines:[{name:'worker-new'}]}},impactHash:'bound-impact-hash',status:'planned',impact:{unknown,observedAt:new Date().toISOString(),issues:unknown?['Provider observation missing']:[],pods:[{namespace:'db',name:'postgres-0',controllerKind:'StatefulSet',controllerName:'postgres',reasons:['Local data is not migrated']}],volumes:[{namespace:'db',pod:'postgres-0',volume:'data',state:'requires-review',pvc:'postgres-data',pv:'pv-db',reclaimPolicy:'Retain',accessModes:['ReadWriteOnce'],csiDriver:'driver.example',nodeAffinity:{required:{nodeSelectorTerms:[]}},attachments:[{name:'attachment-1',nodeName:'worker-old',attached:true,attacher:'driver.example'}],reasons:['Local data is not migrated']}]}});
 await context.route('**/fixture',r=>r.fulfill({contentType:'text/html',body:'<html><body><div id="fixture"></div></body></html>'}));
 await context.route('**/api/**',r=>{const req=r.request(),path=new URL(req.url()).pathname;if(!path.startsWith('/api/'))return r.continue();
  if(path.endsWith('/replacements/plan')){captured=JSON.parse(req.postData());assert.equal(captured.mode,'dead');assert.equal(captured.machineId,'owned-worker');assert.equal(captured.replacement.machines.length,1);return r.fulfill({json:plan()});}
  if(path.endsWith('/resume-plan')){resumed++;assert.equal(req.postData(),null);return r.fulfill({json:{...plan(),id:'resume-plan',resume:true,previousPlanId:'plan',replacementPlan:{id:'existing-child',spec:captured.replacement}}});}
  if(path.endsWith('/replacements')&&req.method()==='POST'){const body=JSON.parse(req.postData());assert.deepEqual(body,{planId:'resume-plan',confirmedName:'worker-old',impactHash:'bound-impact-hash',acknowledgeStorageImpact:true});submitted++;return r.fulfill({json:{id:'job'}});}
  if(path.endsWith('/replacements'))return r.fulfill({json:[{id:'interrupted',nodeName:'worker-old',status:'requires_review'}]});
  if(path.endsWith('/machines'))return r.fulfill({json:[{id:'owned-worker',name:'worker-old',role:'worker',status:'ready',providerId:'p',providerNode:'pve',vmid:117},{id:'cp',name:'cp-old',role:'controlplane',status:'ready'}]});
  if(path.endsWith('/providers'))return r.fulfill({json:[{id:'p',name:'PVE'}]});
  if(path.endsWith('/cluster'))return r.fulfill({json:{name:'test-cluster',kubernetesVersion:'1.37.0'}});
  return r.fulfill({json:{versions:[]}});
 });
 const page=await context.newPage();const pageErrors=[];page.on('pageerror',e=>{pageErrors.push(e.message);console.log('PAGEERROR',e.message)});await page.goto('http://127.0.0.1:5179/fixture');
 await page.evaluate(async()=>{await import('/src/console/console.css');const {createApp}=await import('/node_modules/.vite/deps/vue.js');const {default:View}=await import('/src/console/WorkerReplacementView.vue');const api=await import('/src/api/index.ts');const recovery=await import('/src/console/recovery.ts');api.isAuthenticated.value=true;api.currentUser.value={username:'admin',role:'admin'};recovery.recoveryState.value='normal';const scope=await import('/src/clusterScope.ts');scope.selectCluster('cluster');createApp(View).mount('#fixture');});
 await page.getByLabel('Режим замены',{exact:false}).selectOption('dead');
 assert.equal(await page.getByRole('option',{name:'cp-old',exact:false}).count(),0);
 await page.getByLabel('Источник образа',{exact:false}).selectOption('manual');await page.getByLabel('Talos',{exact:true}).fill('1.14.0');await page.getByLabel('Installer',{exact:true}).fill('ghcr.io/siderolabs/installer:v1.14.0');await page.getByLabel('ISO',{exact:true}).fill('data:iso/talos.iso');
 await page.getByRole('button',{name:'Проверить план замены',exact:true}).click();await page.getByText('Влияние определено не полностью.',{exact:false}).waitFor();assert.equal(await page.getByRole('button',{name:'Подтвердить замену',exact:true}).isDisabled(),true);
 unknown=false;await page.getByRole('button',{name:'Проверить план замены',exact:true}).click();await page.getByText('Влияние определено не полностью.',{exact:false}).waitFor({state:'hidden'});
 await page.locator('.volume-impact>summary').click();await page.getByText('Retain / ReadWriteOnce',{exact:true}).waitFor();await page.getByText('attachment-1',{exact:true}).waitFor();
 await page.locator('.replacement-check input').check();await page.getByLabel('Введите имя старой машины',{exact:false}).fill('worker-old');assert.equal(await page.getByRole('button',{name:'Подтвердить замену',exact:true}).isEnabled(),true);
 await page.getByLabel('Имя новой ноды',{exact:true}).fill('worker-new');assert.equal(await page.locator('.replacement-plan').count(),0);
 await page.getByRole('button',{name:'Сверить и подготовить продолжение',exact:true}).click();await page.getByText('existing-child',{exact:true}).waitFor();assert.equal(resumed,1);assert.equal(submitted,0);assert.equal(await page.locator('.replacement-check input').isChecked(),false);
 await page.locator('.replacement-check input').check();await page.getByLabel('Введите имя старой машины',{exact:false}).fill('worker-old');await page.getByRole('button',{name:'Подтвердить замену',exact:true}).click();assert.equal(submitted,1);
 await page.setViewportSize({width:390,height:844});await page.evaluate(async()=>{const i18n=await import('/src/console/i18n.ts');i18n.setLocale('en');});await page.getByText('Replacement plan',{exact:false}).first().waitFor();
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>window.innerWidth),false);
 assert.deepEqual(pageErrors,[]);await context.close();console.log('Worker replacement UI smoke passed');
}finally{await browser.close();await server.close();}

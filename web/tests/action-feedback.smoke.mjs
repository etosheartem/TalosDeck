import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { createServer } from 'vite';
const server=await createServer({server:{host:'127.0.0.1',port:5181,strictPort:true}});await server.listen();
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
try {
 const page=await browser.newPage();await page.route('**/fixture',r=>r.fulfill({contentType:'text/html',body:'<div></div>'}));await page.goto('http://127.0.0.1:5181/fixture');
 const result=await page.evaluate(async()=>{
  const f=await import('/src/console/actionFeedback.ts');const {setLocale}=await import('/src/console/i18n.ts');
  const job={id:'a',request:{kind:'worker-replace',node:'worker-1'},status:'queued'};
  f.observeJobResponse({...job,status:'succeeded'},false,'c',false);const historical=f.actionFeedback.value;
  f.observeJobResponse(job,true,'c',false);const queued=f.actionState(f.actionFeedback.value.status);
  f.observeJobResponse({...job,status:'succeeded'},false,'other',false);const isolated=f.actionFeedback.value.status;
  f.observeJobResponse({...job,status:'interrupted'},false,'c',false);const interrupted=f.actionState(f.actionFeedback.value.status);
  f.observeJobResponse({...job,status:'succeeded',reconciliationOutcome:'UNKNOWN'},false,'c',false);const unknown=f.actionState(f.actionFeedback.value.status);
  f.observeJobResponse({...job,status:'succeeded'},false,'c',false);const completed=f.actionState(f.actionFeedback.value.status);
  setLocale('en');return {historical,queued,isolated,interrupted,unknown,completed,name:f.actionName(job.request.kind),en:f.actionState('interrupted')};
 });
 assert.equal(result.historical,null);assert.equal(result.queued,'В очереди');assert.equal(result.isolated,'queued');assert.match(result.interrupted,/проверки/);assert.equal(result.unknown,'Результат неизвестен');assert.equal(result.completed,'Завершено');assert.equal(result.name,'Worker replacement');assert.match(result.en,/requires review/);
 await page.route('**/api/clusters/c/jobs',r=>r.abort());
 const failure=await page.evaluate(async()=>{const scope=await import('/src/clusterScope.ts');scope.selectCluster('c');const {request}=await import('/src/console/client.ts');try{await request('/jobs',{method:'POST',body:'{}'});}catch(e){return e.message;}});assert.match(failure,/outcome is unknown/);
 await page.unroute('**/api/clusters/c/jobs');
 const jobs=[{id:'first',request:{kind:'worker-replace'},status:'queued',events:[]},{id:'second',request:{kind:'backup-create'},status:'succeeded',events:[]}];
 await page.route('**/api/clusters/c/jobs',r=>r.fulfill({json:jobs}));
 await page.route('**/api/clusters/c/jobs/*',r=>r.fulfill({json:jobs.find(j=>new URL(r.request().url()).pathname.endsWith('/'+j.id))}));
 await page.evaluate(async()=>{
  document.body.innerHTML='<div id="app"></div>';
  const {createApp}=await import('/node_modules/.vite/deps/vue.js');
  const {default:View}=await import('/src/console/JobsView.vue');
  const f=await import('/src/console/actionFeedback.ts');f.jobFocus.value='second';
  createApp(View,{mode:'jobs'}).mount('#app');
 });
 await page.locator('.execution-log h2').filter({hasText:'Backup creation'}).waitFor();
 await page.evaluate(async()=>{const f=await import('/src/console/actionFeedback.ts');f.jobFocus.value='first';});
 await page.locator('.execution-log h2').filter({hasText:'Worker replacement'}).waitFor();
 console.log('Action feedback and exact job navigation PASS');
} finally {await browser.close();await server.close();}

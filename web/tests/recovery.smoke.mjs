import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { createServer } from 'vite';
const server=await createServer({server:{host:'127.0.0.1',port:5178,strictPort:true}});await server.listen();
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
try{
 const context=await browser.newContext();let status='unavailable';const mutations=[];
 await context.route('**/api/**',route=>{const url=new URL(route.request().url());if(!url.pathname.startsWith('/api/'))return route.continue();if(route.request().method()!=='GET')mutations.push(url.pathname);if(url.pathname==='/api/recovery/status')return route.fulfill({status:status==='unavailable'?503:200,json:{safeMode:status==='safe',requiresReview:status==='safe',automationPaused:status==='paused',automaticResume:false}});return route.fulfill({json:url.pathname==='/api/auth/me'?{user:{username:'admin',role:'admin'}}:{clusters:[],oidc:{enabled:false}}});});
 const page=await context.newPage();page.on('pageerror',e=>console.log('PAGEERROR',e.message));await page.goto('http://127.0.0.1:5178/');await page.getByText('Не удалось проверить режим восстановления.',{exact:false}).waitFor();assert.equal(await page.locator('.console-app').count(),0);
 status='safe';await page.getByRole('button',{name:'Повторить',exact:true}).click();await page.getByText('Безопасный режим после восстановления',{exact:true}).waitFor();assert.equal(await page.locator('.recovery-banner button').count(),0);
 await page.evaluate(()=>localStorage.setItem('talosdeck.console.language','en'));await page.reload();await page.getByText('Recovery safe mode',{exact:true}).waitFor();await page.getByText('Mode restrictions',{exact:true}).click();await page.getByText('Nothing resumes automatically.',{exact:false}).waitFor();await page.getByText('Mode restrictions',{exact:true}).click();await page.evaluate(()=>{document.querySelector('main').style.minHeight='2500px';window.scrollTo(0,1200);});
 assert((await page.locator('.management-mode').boundingBox()).y>=0);
 await page.setViewportSize({width:390,height:844});
 assert((await page.locator('.management-mode').boundingBox()).y>=0);
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 status='paused';await page.reload();await page.getByText('Automation paused after recovery',{exact:true}).waitFor();assert.equal(await page.locator('.recovery-safe').count(),0);
 await page.getByText('Mode restrictions',{exact:true}).click();await page.getByText('Manual management',{exact:false}).waitFor();
 status='normal';await page.reload();await page.getByText('Normal mode',{exact:true}).waitFor();assert.equal(await page.locator('.mode-details').count(),0);
 assert.deepEqual(mutations,[]);await context.close();
 const jc=await browser.newContext();const id='e5a0d9ea-9b91-45c1-8f54-472eeb8c12d2';let calls=0;
 const job={id,request:{kind:'cluster-create'},user:'admin',status:'interrupted',createdAt:new Date().toISOString(),updatedAt:new Date().toISOString(),step:'create-vm',reviewed:false,reconciliationOutcome:'UNKNOWN',intents:[{id:'create:machine',action:'create',executorEpoch:42,outcome:'UNKNOWN',identity:{providerId:'provider',resourceId:'pve/117',generation:'machine',ownerId:'cluster'}}]};
 await jc.route('**/fixture',r=>r.fulfill({contentType:'text/html',body:'<html><body><div id="fixture"></div></body></html>'}));
 await jc.route('**/api/**',r=>{const req=r.request(),path=new URL(req.url()).pathname;if(!path.startsWith('/api/'))return r.continue();if(req.method()==='POST'){assert.equal(path,`/api/provision/jobs/${id}/reconcile`);assert.equal(req.postData(),null);calls++;job.reconciliationOutcome='succeeded';job.intents[0].outcome='succeeded';job.intents[0].evidence={state:'exists',observedAt:new Date().toISOString(),source:'provider_ownership_verified'};return r.fulfill({json:job});}return r.fulfill({json:path.endsWith('/jobs')?[job]:job});});
 const jp=await jc.newPage();await jp.goto('http://127.0.0.1:5178/fixture');
 await jp.evaluate(async()=>{const {createApp}=await import('/node_modules/.vite/deps/vue.js');const {default:JobsView}=await import('/src/console/JobsView.vue');const api=await import('/src/api/index.ts');const recovery=await import('/src/console/recovery.ts');api.isAuthenticated.value=true;api.currentUser.value={username:'admin',role:'admin'};recovery.recoveryState.value='safe';createApp(JobsView,{mode:'jobs',global:true}).mount('#fixture');});
 await jp.getByRole('button',{name:'Сверить с провайдером',exact:true}).click();await jp.getByText('Подтверждение результата',{exact:true}).waitFor();assert.equal(calls,1);assert.equal(await jp.getByRole('button',{name:'Проверить прерывание',exact:true}).count(),0);await jp.locator('.intent-evidence summary').click();await jp.getByText('Проверена принадлежность машины',{exact:true}).waitFor();await jp.evaluate(async()=>{const api=await import('/src/api/index.ts');api.currentUser.value={username:'viewer',role:'viewer'};});await jp.getByRole('button',{name:'Сверить с провайдером',exact:true}).waitFor({state:'hidden'});await jc.close();
 // TD-30: management-plane protection history and the audited automation resume.
 const rc=await browser.newContext();let recoveryStatus={safeMode:false,requiresReview:false,automationPaused:true,automaticResume:false,automationResumeRecorded:false};
 const created='2026-09-14T03:00:00Z',uploaded='2026-09-14T03:02:00Z',drilled='2026-09-15T01:00:00Z';
 let protection={historyConfigured:true,unresolvedObservation:true,lastBackupAt:created,lastBackupUploadedAt:uploaded,recoveryPointSeconds:93600,lastRestoreTestedAt:drilled,lastRestoreTestedForBackupCreatedAt:created,lastVerificationAt:drilled,records:[
  {id:'r3',kind:'backup',outcome:'unknown',observedAt:drilled,backupCreatedAt:drilled,target:'s3://dr/abc.tdr',error:'upload response lost'},
  {id:'r2',kind:'drill',outcome:'succeeded',observedAt:drilled,backupCreatedAt:created,checksumVerifiedAt:drilled,decryptVerifiedAt:drilled,schemaVerifiedAt:drilled,restoreTestedAt:drilled,target:'s3://dr/one.tdr'},
  {id:'r1',kind:'backup',outcome:'succeeded',observedAt:uploaded,backupCreatedAt:created,uploadedAt:uploaded,checksumVerifiedAt:uploaded,decryptVerifiedAt:uploaded,schemaVerifiedAt:uploaded,target:'s3://dr/one.tdr'}]};
 const resumes=[];
 await rc.route('**/fixture',r=>r.fulfill({contentType:'text/html',body:'<html><body><div id="fixture"></div></body></html>'}));
 await rc.route('**/api/**',r=>{const req=r.request(),path=new URL(req.url()).pathname;if(!path.startsWith('/api/'))return r.continue();
  if(path==='/api/recovery/status')return r.fulfill({json:recoveryStatus});
  if(path==='/api/recovery/protection')return r.fulfill({json:protection});
  if(path==='/api/recovery/automation/resume'){assert.equal(req.method(),'POST');resumes.push(JSON.parse(req.postData()).reason);recoveryStatus={...recoveryStatus,automationResumeRecorded:true};return r.fulfill({json:{resumedAt:new Date().toISOString(),automationPaused:true,restartRequired:true,message:'Automation resume recorded. Schedules, collectors and notification delivery start after the next TalosDeck restart.'}});}
  return r.fulfill({json:{}});});
 const rp=await rc.newPage();rp.on('pageerror',e=>console.log('PAGEERROR',e.message));await rp.goto('http://127.0.0.1:5178/fixture');
 const mountRecovery=async()=>rp.evaluate(async()=>{const {createApp}=await import('/node_modules/.vite/deps/vue.js');await import('/src/console/console.css');const {default:RecoveryView}=await import('/src/console/RecoveryView.vue');const api=await import('/src/api/index.ts');api.isAuthenticated.value=true;api.currentUser.value={username:'admin',role:'admin'};if(window.__app)window.__app.unmount();document.querySelector('#fixture').innerHTML='';window.__app=createApp(RecoveryView);window.__app.mount('#fixture');});
 await mountRecovery();
 await rp.getByText('Восстановление TalosDeck',{exact:true}).waitFor();
 await rp.getByText('Это не резервные копии etcd подключённых кластеров.',{exact:false}).waitFor();
 // A drill an hour ago must not present the 26-hour-old archive as a fresh recovery point.
 const rpo=await rp.locator('.summary-strip span',{hasText:'Фактический RPO'}).innerText();
 assert.match(rpo,/26 ч\./);
 await rp.getByText('результат переноса не доказан',{exact:false}).waitFor();
 const drillRow=rp.locator('tbody tr').nth(1);
 assert.match(await drillRow.innerText(),/Проверочное восстановление/);
 assert.match(await rp.locator('tbody tr').nth(0).innerText(),/Не доказано/);
 // The drill row proves a restore but must show no upload of its own.
 const cells=await drillRow.locator('td').allInnerTexts();
 assert.equal(cells[4].trim(),'—','drill claimed an upload');assert.notEqual(cells[5].trim(),'—','drill recorded no checksum verification');
 await rp.getByText('не создаёт новую копию, не сдвигает RPO',{exact:false}).waitFor();
 // Resuming automation is an explicit, reasoned decision that needs a restart.
 const resumeButton=rp.getByRole('button',{name:'Записать возобновление автоматизации',exact:true});
 assert.equal(await resumeButton.isDisabled(),true);
 await rp.getByRole('textbox',{name:'Основание возобновления'}).fill('Прерванные задания разобраны');
 await resumeButton.click();
 await rp.getByText('Schedules, collectors and notification delivery start after the next TalosDeck restart.',{exact:false}).waitFor();
 await rp.getByText('Возобновление записано и ожидает перезапуска',{exact:false}).waitFor();
 assert.deepEqual(resumes,['Прерванные задания разобраны']);
 await resumeButton.waitFor({state:'hidden'});
 // Safe mode offers no resume at all, and unconfigured history claims nothing.
 recoveryStatus={safeMode:true,requiresReview:true,automationPaused:true,automaticResume:false,automationResumeRecorded:false};
 protection={historyConfigured:false,unresolvedObservation:false,records:[]};
 await mountRecovery();
 await rp.getByText('Безопасный режим',{exact:false}).waitFor();
 await rp.getByText('История проверок не настроена',{exact:false}).waitFor();
 assert.equal(await rp.getByRole('button',{name:'Записать возобновление автоматизации',exact:true}).count(),0);
 assert.match(await rp.locator('.summary-strip span',{hasText:'Последняя доказанная копия'}).innerText(),/Неизвестно/);
 await rp.evaluate(()=>localStorage.setItem('talosdeck.console.language','en'));await rp.reload();await mountRecovery();
 await rp.getByText('TalosDeck recovery',{exact:true}).waitFor();
 await rp.getByText('Verification history is not configured',{exact:false}).waitFor();
 // The wide evidence table must scroll inside its own container, not the page.
 assert.equal(await rp.locator('.table-scroll table').count(),1);
 await rp.setViewportSize({width:390,height:844});
 assert(await rp.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 assert.deepEqual(resumes,['Прерванные задания разобраны']);
 await rc.close();
 console.log('Recovery UI smoke passed');
}finally{await browser.close();await server.close();}

import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { createServer } from 'vite';
const server=await createServer({server:{host:'127.0.0.1',port:5178,strictPort:true}});await server.listen();
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',headless:true,args:['--no-sandbox']});
try{
 const context=await browser.newContext();let status='unavailable';const mutations=[];
 await context.route('**/api/**',route=>{const url=new URL(route.request().url());if(!url.pathname.startsWith('/api/'))return route.continue();if(route.request().method()!=='GET')mutations.push(url.pathname);if(url.pathname==='/api/recovery/status')return route.fulfill({status:status==='unavailable'?503:200,json:{safeMode:true,requiresReview:true,automaticResume:false}});return route.fulfill({json:url.pathname==='/api/auth/me'?{user:{username:'admin',role:'admin'}}:{clusters:[],oidc:{enabled:false}}});});
 const page=await context.newPage();page.on('pageerror',e=>console.log('PAGEERROR',e.message));await page.goto('http://127.0.0.1:5178/');await page.getByText('Не удалось проверить режим восстановления.',{exact:false}).waitFor();assert.equal(await page.locator('.console-app').count(),0);
 status='safe';await page.getByRole('button',{name:'Повторить',exact:true}).click();await page.getByText('Безопасный режим после восстановления',{exact:true}).waitFor();assert.equal(await page.locator('.recovery-banner button').count(),0);
 await page.evaluate(()=>localStorage.setItem('talosdeck.console.language','en'));await page.reload();await page.getByText('Recovery safe mode',{exact:true}).waitFor();await page.getByText('Nothing resumes automatically.',{exact:false}).waitFor();assert.deepEqual(mutations,[]);await context.close();console.log('Recovery UI smoke passed');
}finally{await browser.close();await server.close();}

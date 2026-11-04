// Two browsers on one board: concurrent first open, then an edit made while
// the server is down must reach the other browser and the store after restart.
// usage: node e2e/restart.mjs <baseURL> '<cmd to stop/start the server, {phase}=stop|start>'
import { chromium } from '@playwright/test';
import { execSync } from 'node:child_process';

const [base, restartCmd] = process.argv.slice(2);
const created = await fetch(`${base}/api/v1/boards`, { method: 'POST', body: JSON.stringify({ title: 'restart test' }) }).then(r => r.json());
const board = created.id;
const browser = await chromium.launch();
const open = async (name, wait = true) => {
  const page = await (await browser.newContext({ viewport: { width: 1200, height: 800 } })).newPage();
  page.on('pageerror', e => console.log(`[${name}] pageerror`, e.message));
  await page.addInitScript(n => localStorage.setItem('aeboard.name', n), name);
  const nav = page.goto(`${base}/b/${board}`);
  if (wait) { await nav; await page.waitForSelector('affine-edgeless-root', { timeout: 30000 }); }
  return { page, nav };
};
const stats = page => page.evaluate(() => {
  const store = window.aeboard.editor.doc;
  const surface = store.getBlocksByFlavour('affine:surface')[0]?.model;
  const flavours = {};
  for (const b of store.getAllModels()) flavours[b.flavour] = (flavours[b.flavour] ?? 0) + 1;
  return { flavours, elements: surface ? surface.elementModels.length : -1, status: window.aeboard.doc.provider?.wsconnected };
});
const draw = async (page, x, y) => {
  await page.mouse.click(x - 100, y - 100);
  await page.keyboard.press('s');
  await page.mouse.move(x, y); await page.mouse.down(); await page.mouse.move(x + 80, y + 60, { steps: 6 }); await page.mouse.up();
  await page.keyboard.press('Escape');
};

// 1. concurrent first open
const [a, b] = await Promise.all([open('alice'), open('bob')]);
await a.page.waitForTimeout(1500);
console.log('race A', JSON.stringify(await stats(a.page)), 'B', JSON.stringify(await stats(b.page)));

// 2. offline edit across restart: kill server, draw on A while down, restart, B must get it
await draw(a.page, 300, 300);
await a.page.waitForTimeout(3000); // let ygo's 2s coalescing window flush
execSync(restartCmd.replace('{phase}', 'stop'));
await a.page.waitForTimeout(500);
await draw(a.page, 600, 300);
console.log('offline A', JSON.stringify(await stats(a.page)));
execSync(restartCmd.replace('{phase}', 'start'));
await a.page.waitForTimeout(6000); // y-websocket reconnect backoff
console.log('after restart A', JSON.stringify(await stats(a.page)), 'B', JSON.stringify(await stats(b.page)));
const c = await open('carol');
await c.page.waitForTimeout(1000);
console.log('fresh C', JSON.stringify(await stats(c.page)));
await browser.close();

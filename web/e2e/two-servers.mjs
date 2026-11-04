// Two aenotes servers over one git remote: a board made on A shows up on B,
// and shapes drawn on each reach the other's open board without a reload.
// usage: node e2e/two-servers.mjs <baseA> <baseB>
import { chromium } from '@playwright/test';

const [baseA, baseB] = process.argv.slice(2);
const sleep = ms => new Promise(r => setTimeout(r, ms));
const board = await fetch(`${baseA}/api/v1/boards`, { method: 'POST', body: '{"title":"two servers"}' }).then(r => r.json());
const t0 = Date.now();
let onB = false;
while (Date.now() - t0 < 30000) {
  const list = await fetch(`${baseB}/api/v1/boards`).then(r => r.json());
  if (list.boards.some(b => b.id === board.id)) { onB = true; break; }
  await sleep(500);
}
console.log(`board on B: ${onB} after ${Math.round((Date.now() - t0) / 1000)}s`);

const browser = await chromium.launch();
const open = async base => {
  const p = await (await browser.newContext({ viewport: { width: 1200, height: 800 } })).newPage();
  p.on('pageerror', e => console.log('pageerror', e.message));
  await p.goto(`${base}/b/${board.id}`);
  await p.waitForSelector('affine-edgeless-root');
  await p.waitForTimeout(500);
  return p;
};
const draw = async (p, x, y) => {
  await p.mouse.click(x - 100, y - 100);
  await p.keyboard.press('s');
  await p.mouse.move(x, y); await p.mouse.down(); await p.mouse.move(x + 80, y + 60, { steps: 6 }); await p.mouse.up();
  await p.keyboard.press('Escape');
};
const count = p => p.evaluate(() => window.aenotes.editor.doc.getBlocksByFlavour('affine:surface')[0].model.elementModels.length);
const waitCount = async (p, n) => {
  const t = Date.now();
  while (Date.now() - t < 40000) { if ((await count(p)) >= n) return Math.round((Date.now() - t) / 1000); await sleep(500); }
  return -1;
};
const a = await open(baseA);
const b = await open(baseB);
await draw(a, 300, 300);
console.log('A shape reached B in', await waitCount(b, 1), 's');
await draw(b, 600, 300);
console.log('B shape reached A in', await waitCount(a, 2), 's');
console.log('final A/B', await count(a), await count(b));
await browser.close();

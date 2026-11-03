// Board list + creation + concurrent first open (seed) + tag chips.
import { chromium } from '@playwright/test';

const [base, out = '.'] = process.argv.slice(2);
const browser = await chromium.launch();
const page = async () => {
  const p = await (await browser.newContext({ viewport: { width: 1200, height: 800 } })).newPage();
  p.on('pageerror', e => console.log('pageerror', e.message));
  return p;
};
const api = (method, path, body) =>
  fetch(base + '/api/v1' + path, { method, headers: { 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) }).then(r => r.json());

const a = await page();
await a.goto(base + '/');
console.log('empty state:', await a.locator('.empty').textContent());

// Create through the UI.
await a.getByRole('button', { name: 'New board' }).click();
await a.getByPlaceholder('Board title').fill('Q4 roadmap');
await a.getByPlaceholder('Tags, comma-separated').fill('product, q4');
await a.getByRole('button', { name: 'Create' }).click();
await a.waitForURL(/\/b\/[0-9a-z]{10}$/);
await a.waitForSelector('affine-edgeless-root', { timeout: 30000 });
const id = a.url().split('/').pop();
console.log('created', id);

// A second brand-new board opened by two browsers at the same instant.
const fresh = await api('POST', '/boards', { title: 'Retro', tags: ['team'] });
await api('POST', '/boards', { title: 'Scratch', tags: [] });
const [b, c] = [await page(), await page()];
await Promise.all([b.goto(`${base}/b/${fresh.id}`), c.goto(`${base}/b/${fresh.id}`)]);
await Promise.all([b.waitForSelector('affine-edgeless-root'), c.waitForSelector('affine-edgeless-root')]);
await b.waitForTimeout(1500);
const roots = p => p.evaluate(() => window.aeboard.editor.doc.getAllModels().map(m => m.flavour).join(','));
console.log('concurrent open B:', await roots(b), '| C:', await roots(c));

// List + chips.
await a.goto(base + '/');
await a.waitForSelector('.board-card');
const titles = () => a.locator('.board-title').allTextContents();
console.log('all:', await titles());
await a.getByRole('button', { name: 'team', exact: true }).click();
console.log('click team:', await titles());
await a.getByRole('button', { name: 'q4', exact: true }).click({ modifiers: ['Shift'] });
console.log('shift q4:', await titles());
await a.screenshot({ path: `${out}/list.png` });
await a.getByRole('button', { name: 'team', exact: true }).click({ modifiers: ['Shift'] });
console.log('shift team off:', await titles());
await a.getByRole('button', { name: 'q4', exact: true }).click();
console.log('click only selected q4 -> clear:', await titles());
await a.goto(`${base}/b/${id}`);
await a.waitForSelector('affine-edgeless-root');
await a.screenshot({ path: `${out}/board.png` });
await browser.close();

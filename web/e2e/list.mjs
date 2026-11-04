// The start page: empty state, creating a board through the UI, and two
// browsers opening a brand-new board at the same instant (one root, from the
// seed). Folders have their own script (folders.mjs).
// usage: node e2e/list.mjs <baseURL>  (a server with no documents yet)
import { chromium } from '@playwright/test';

const [base] = process.argv.slice(2);
const browser = await chromium.launch();
const page = async () => {
  const p = await (await browser.newContext({ viewport: { width: 1200, height: 800 } })).newPage();
  p.on('pageerror', e => console.log('pageerror', e.message));
  return p;
};
const a = await page();
await a.goto(base + '/');
console.log('empty state:', await a.locator('.empty').textContent());
await a.getByRole('button', { name: 'New board' }).click();
await a.getByPlaceholder('Board title').fill('Q4 roadmap');
await a.getByRole('button', { name: 'Create' }).click();
await a.waitForURL(/\/b\/[0-9a-z]{10}$/);
await a.waitForSelector('affine-edgeless-root', { timeout: 30000 });
console.log('created', a.url().split('/').pop());

const fresh = await fetch(`${base}/api/v1/boards`, { method: 'POST', body: '{"title":"Retro"}' }).then(r => r.json());
const [b, c] = [await page(), await page()];
await Promise.all([b.goto(`${base}/b/${fresh.id}`), c.goto(`${base}/b/${fresh.id}`)]);
await Promise.all([b.waitForSelector('affine-edgeless-root'), c.waitForSelector('affine-edgeless-root')]);
await b.waitForTimeout(1500);
const roots = p => p.evaluate(() => window.aeboard.editor.doc.getAllModels().map(m => m.flavour).join(','));
console.log('concurrent open B:', await roots(b), '| C:', await roots(c));
await browser.close();

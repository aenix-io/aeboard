// Ordering in the sidebar and live structure: two browsers with the sidebar
// open; A reorders by dragging, creates a folder and types a page title, and
// B's tree follows each time without a reload.
// usage: node e2e/live.mjs <baseURL>   (an empty server)
import { chromium } from '@playwright/test';

const [base] = process.argv.slice(2);
const api = (method, path, body) =>
  fetch(base + '/api/v1' + path, { method, headers: { 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) }).then(r => (r.status === 204 ? null : r.json()));
for (const t of ['Alpha', 'Beta', 'Gamma']) await api('POST', '/boards', { title: t, mode: 'page' });
const page = (await api('POST', '/boards', { mode: 'page' })).id;

const browser = await chromium.launch();
const open = async () => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.addInitScript(() => localStorage.setItem('aenotes.sidebar', 'open'));
  const p = await ctx.newPage();
  p.on('pageerror', e => console.log('pageerror', e.message));
  await p.goto(base + '/');
  await p.waitForSelector('.sidebar .sb-row');
  return p;
};
const tree = p => p.locator('.sidebar .sb-name').allTextContents();
const waitFor = async (p, want, label) => {
  const t0 = Date.now();
  while (Date.now() - t0 < 8000) {
    const got = (await tree(p)).join(', ');
    if (want(got)) return console.log(`${label}: ok in ${Date.now() - t0}ms (${got})`);
    await p.waitForTimeout(100);
  }
  console.log(`${label}: FAILED (${(await tree(p)).join(', ')})`);
};
const a = await open();
const b = await open();
console.log('initial:', (await tree(a)).join(', '));

// Gamma above Alpha: drop on Alpha's top edge.
const alpha = a.locator('.sidebar .sb-row', { hasText: 'Alpha' });
await a.locator('.sidebar .sb-row', { hasText: 'Gamma' }).dragTo(alpha, { targetPosition: { x: 40, y: 3 } });
await waitFor(a, t => t.startsWith('Gamma, Alpha, Beta'), 'A reordered');
await waitFor(b, t => t.startsWith('Gamma, Alpha, Beta'), 'B sees the new order');

// Beta between Gamma and Alpha: drop on Gamma's bottom edge.
const gamma = a.locator('.sidebar .sb-row', { hasText: 'Gamma' });
const box = await gamma.boundingBox();
await a.locator('.sidebar .sb-row', { hasText: 'Beta' }).dragTo(gamma, { targetPosition: { x: 40, y: box.height - 3 } });
await waitFor(b, t => t.startsWith('Gamma, Beta, Alpha'), 'B sees the second move');

// A folder created on A.
await a.locator('.sb-head').getByRole('button', { name: 'Add' }).click();
await a.getByRole('menuitem', { name: 'New folder' }).click();
await a.locator('.sidebar .sb-input').fill('Shared folder');
await a.keyboard.press('Enter');
await waitFor(b, t => t.includes('Shared folder'), 'B sees the new folder');

// A title typed in a page on A.
await a.goto(`${base}/b/${page}`);
await a.waitForSelector('affine-page-root');
await a.locator('doc-title [contenteditable="true"]').click();
await a.keyboard.type('Live title');
await waitFor(b, t => t.includes('Live title'), 'B sees the typed title');
await browser.close();

// The sidebar: hidden by default, toggled from the header and remembered by
// the browser; the tree; "+" creates a page in a folder and opens it; drag
// and drop within the tree and back to the top level.
// usage: node e2e/sidebar.mjs <baseURL> <outDir>
import { chromium } from '@playwright/test';

const [base, out = '.'] = process.argv.slice(2);
const api = (method, path, body) =>
  fetch(base + '/api/v1' + path, { method, headers: { 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) }).then(r => (r.status === 204 ? null : r.json()));
const where = async id => (await api('GET', '/boards')).boards.find(b => b.id === id)?.folder;

const work = await api('POST', '/folders', { name: 'Work', parent: '' });
const roadmap = await api('POST', '/boards', { title: 'Roadmap' });
await api('POST', '/boards', { title: 'Notes in work', mode: 'page', folder: work.id });

const browser = await chromium.launch();
const p = await (await browser.newContext({ viewport: { width: 1280, height: 800 } })).newPage();
p.on('pageerror', e => console.log('pageerror', e.message));
await p.goto(base + '/');
await p.waitForSelector('.board-card');
console.log('hidden by default:', (await p.locator('.sidebar').count()) === 0);
await p.getByRole('button', { name: 'Show sidebar' }).click();
await p.waitForSelector('.sidebar');
await p.reload();
await p.waitForSelector('.board-card');
console.log('still open after reload:', (await p.locator('.sidebar').count()) === 1);

// Expand Work: its page shows.
await p.getByRole('button', { name: 'Expand Work' }).click();
console.log('tree:', (await p.locator('.sidebar .sb-name').allTextContents()).join(', '));

// "+" on Work → New page: created in Work and opened.
const workRow = p.locator('.sidebar .sb-row', { hasText: 'Work' }).first();
await workRow.hover();
await workRow.getByRole('button', { name: 'Add to folder' }).click();
await p.getByRole('menuitem', { name: 'New page' }).click();
await p.waitForURL(/\/b\//);
await p.waitForSelector('affine-page-root');
const created = p.url().split('/').pop();
console.log('new page in Work, opened:', (await where(created)) === work.id, '| active row:', await p.locator('.sidebar .sb-active').innerText());

// Drag Roadmap onto Work in the tree, then back to the top level (the space below the tree).
await p.locator('.sidebar .sb-row', { hasText: 'Roadmap' }).dragTo(workRow);
await p.waitForTimeout(800);
console.log('Roadmap into Work by drag:', (await where(roadmap.id)) === work.id);
await p.locator('.sidebar .sb-row', { hasText: 'Roadmap' }).dragTo(p.locator('.sidebar .sb-rest'));
await p.waitForTimeout(800);
console.log('Roadmap back to the top:', (await where(roadmap.id)) === '');
await p.screenshot({ path: `${out}/sidebar-doc.png` });

// The header toggle hides it again.
await p.getByRole('button', { name: 'Hide sidebar' }).click();
console.log('hidden again:', (await p.locator('.sidebar').count()) === 0);
await p.goto(base + '/');
await p.getByRole('button', { name: 'Show sidebar' }).click();
await p.screenshot({ path: `${out}/sidebar-list.png` });
await browser.close();

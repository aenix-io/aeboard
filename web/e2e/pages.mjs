// Pages: create one, write in it from two browsers, link a board with @ and
// follow the link, see a board as a page, create a new page from the menu.
// usage: node e2e/pages.mjs <baseURL> <outDir>
import { chromium } from '@playwright/test';

const [base, out = '.'] = process.argv.slice(2);
const browser = await chromium.launch();
const open = async (name, path) => {
  const p = await (await browser.newContext({ viewport: { width: 1280, height: 860 } })).newPage();
  p.on('pageerror', e => console.log(`[${name}] pageerror`, e.message));
  p.on('console', m => m.type() === 'error' && console.log(`[${name}] console`, m.text().slice(0, 200)));
  await p.addInitScript(n => localStorage.setItem('aeboard.name', n), name);
  if (path) await p.goto(base + path);
  return p;
};
const sleep = ms => new Promise(r => setTimeout(r, ms));
const blocks = p => p.evaluate(() => window.aeboard.editor.doc.getAllModels().map(m => m.flavour));
const text = p => p.evaluate(() => window.aeboard.editor.doc.getAllModels().filter(m => m.flavour === 'affine:paragraph').map(m => m.props.text.toString()).join('|'));

// 1. New page through the list.
const a = await open('alice', '/');
await a.getByRole('button', { name: 'New page' }).click();
await a.getByPlaceholder('Page title').fill('Meeting notes');
await a.getByRole('button', { name: 'Create' }).click();
await a.waitForURL(/\/b\/[0-9a-z]{10}$/);
await a.waitForSelector('affine-page-root', { timeout: 30000 });
const pageId = a.url().split('/').pop();
console.log('page blocks:', (await blocks(a)).join(','));
await a.locator('affine-paragraph [contenteditable="true"]').first().click();
await a.keyboard.type('Agenda: ship pages');
const b = await open('bob', `/b/${pageId}`);
await b.waitForSelector('affine-page-root');
await sleep(1500);
console.log('B sees text:', await text(b));

// 2. @-link to the board, then follow it.
await a.keyboard.press('Enter');
await a.keyboard.type('@Road');
await sleep(800);
await a.screenshot({ path: `${out}/pages-menu.png` });
await a.keyboard.press('Enter');
await sleep(800);
const refs = await a.locator('affine-reference').count();
console.log('references inserted:', refs);
await a.screenshot({ path: `${out}/pages-a.png` });
if (refs) {
  await a.locator('affine-reference').first().click();
  await sleep(1500);
  console.log('after click URL:', a.url().replace(base, ''), 'edgeless root:', await a.locator('affine-edgeless-root').count());
}

// 3. The board as a page.
await a.getByRole('tab', { name: 'Page' }).click();
await sleep(1000);
console.log('board in page view: page root', await a.locator('affine-page-root').count(), 'URL', a.url().replace(base, ''));
await a.getByRole('tab', { name: 'Board' }).click();

// 4. New page from the @ menu, in the notes page.
await a.goto(`${base}/b/${pageId}`);
await a.waitForSelector('affine-page-root');
await a.locator('affine-paragraph [contenteditable="true"]').last().click();
await a.keyboard.press('End');
await a.keyboard.press('Enter');
await a.keyboard.type('@Follow-ups');
await sleep(800);
await a.screenshot({ path: `${out}/pages-new-menu.png` });
const newItem = a.locator('.linked-doc-popover icon-button, .linked-doc-popover .linked-doc-popover-item').filter({ hasText: /New|Create/ }).first();
console.log('new-doc items:', await newItem.count());
if (await newItem.count()) {
  await newItem.click();
  await sleep(2500);
}
const list = await fetch(`${base}/api/v1/boards`).then(r => r.json());
console.log('catalog:', list.boards.map(d => `${d.mode}:${d.title || '(untitled)'}`).join(' | '));
const created = list.boards.find(d => d.title === 'Follow-ups');
if (created) {
  const c = await open('carol', `/b/${created.id}`);
  await c.waitForSelector('affine-page-root', { timeout: 20000 });
  await sleep(800);
  console.log('new page blocks:', (await blocks(c)).join(','));
}
await browser.close();

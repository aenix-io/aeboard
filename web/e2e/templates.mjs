// The template panel: a sticker inserted by A reaches B as an image whose
// attachment lives on the server; a board template inserts too.
import { chromium } from '@playwright/test';

const [base, out = '.'] = process.argv.slice(2);
const board = await fetch(`${base}/api/v1/boards`, { method: 'POST', body: '{"title":"templates"}' }).then(r => r.json());
const browser = await chromium.launch();
const open = async name => {
  const p = await (await browser.newContext({ viewport: { width: 1280, height: 860 } })).newPage();
  p.on('pageerror', e => console.log(`[${name}] pageerror`, e.message));
  await p.addInitScript(n => localStorage.setItem('aenotes.name', n), name);
  await p.goto(`${base}/b/${board.id}`);
  await p.waitForSelector('affine-edgeless-root');
  await p.waitForTimeout(800);
  return p;
};
const flavours = p => p.evaluate(() => window.aenotes.editor.doc.getAllModels().map(m => m.flavour));
const dragFirst = async p => {
  const box = await p.locator('edgeless-templates-panel .template-item').first().boundingBox();
  await p.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await p.mouse.down();
  await p.mouse.move(400, 400, { steps: 12 });
  await p.mouse.up();
};
const a = await open('alice');
const b = await open('bob');

await a.locator('edgeless-template-button').click();
await a.waitForSelector('edgeless-templates-panel');
await a.waitForTimeout(1500);
const cats = await a.locator('edgeless-templates-panel .category-entry').allTextContents();
console.log('categories:', cats.map(s => s.trim()));
await a.screenshot({ path: `${out}/templates-panel.png` });
await dragFirst(a);
await a.waitForTimeout(2500);
console.log('A after sticker:', (await flavours(a)).join(','));
console.log('B after sticker:', (await flavours(b)).join(','));
const img = await b.evaluate(async () => {
  const m = window.aenotes.editor.doc.getAllModels().find(m => m.flavour === 'affine:image');
  return m ? m.props.sourceId : null;
});
const blob = img && (await fetch(`${base}/api/v1/boards/${board.id}/blobs/${encodeURIComponent(img)}`));
console.log('sticker blob on server:', img, blob?.status, blob?.headers.get('content-type'));
await b.waitForTimeout(1000);
await b.screenshot({ path: `${out}/templates-b.png` });

// A board template (Brainstorming → first).
await a.keyboard.press('Escape');
await a.mouse.click(200, 200);
await a.locator('edgeless-template-button').click();
await a.waitForSelector('edgeless-templates-panel');
await a.locator('edgeless-templates-panel .category-entry', { hasText: 'Brainstorming' }).click();
await a.waitForTimeout(1500);
await dragFirst(a);
await a.waitForTimeout(3000);
const count = p => p.evaluate(() => {
  const d = window.aenotes.editor.doc;
  const surface = d.getBlocksByFlavour('affine:surface')[0].model;
  return d.getAllModels().length + surface.elementModels.length;
});
console.log('blocks+elements A/B after board template:', await count(a), await count(b));
await b.screenshot({ path: `${out}/templates-b2.png` });
await browser.close();

// Compatibility spike: two browsers on one board, edits + cursors + selection.
// usage: node e2e/spike.mjs <baseURL> <board> [phase]
//   phase "edit"   — A draws a shape through the UI, B must see it, cursors/selection cross over
//   phase "verify" — reopen, print element/block counts (run after a server restart)
import { chromium } from '@playwright/test';

const [base, board, phase = 'edit'] = process.argv.slice(2);
const browser = await chromium.launch();
const open = async name => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } });
  const page = await ctx.newPage();
  page.on('pageerror', e => console.log(`[${name}] pageerror`, e.message));
  page.on('console', m => m.type() === 'error' && console.log(`[${name}] console`, m.text()));
  await page.goto(`${base}/?board=${board}&name=${name}`);
  await page.waitForSelector('affine-edgeless-root', { timeout: 30000 });
  await page.waitForTimeout(1000);
  return page;
};
const stats = page =>
  page.evaluate(() => {
    const s = window.aeboard;
    const store = s.editor.doc;
    const surface = store.getBlocksByFlavour('affine:surface')[0]?.model;
    const elements = surface ? [...surface.elementModels].map(e => `${e.type}:${e.id}`) : [];
    const flavours = {};
    for (const b of store.getAllModels()) flavours[b.flavour] = (flavours[b.flavour] ?? 0) + 1;
    const aw = [...s.provider.awareness.getStates().entries()].map(([id, st]) => ({
      id,
      user: st.user?.name,
      sel: Object.keys(st.selectionV2 ?? {}).length ? st.selectionV2 : undefined,
    }));
    const w = document.querySelector('affine-edgeless-remote-selection-widget');
    const root = w?.shadowRoot ?? w;
    return { elements, flavours, aw,
      cursors: [...(root?.querySelectorAll('.remote-cursor') ?? [])].map(c => c.textContent.trim()),
      remoteRects: root?.querySelectorAll('.remote-rect').length ?? 0 };
  });

if (phase === 'edit') {
  const a = await open('alice');
  const b = await open('bob');
  // Draw a rectangle on A through the real toolbar: shape tool + drag.
  await a.mouse.click(300, 200);
  await a.keyboard.press('s');
  await a.mouse.move(400, 300);
  await a.mouse.down();
  await a.mouse.move(550, 420, { steps: 8 });
  await a.mouse.up();
  await a.waitForTimeout(300);
  // Move A's pointer around so a cursor gets broadcast.
  for (let i = 0; i < 5; i++) {
    await a.mouse.move(600 + i * 40, 500, { steps: 4 });
    await a.waitForTimeout(100);
  }
  console.log('A local', JSON.stringify(await a.evaluate(() => window.aeboard.provider.awareness.getLocalState())));
  await b.waitForTimeout(1500);
  console.log('A', JSON.stringify(await stats(a)));
  console.log('B', JSON.stringify(await stats(b)));
  await b.screenshot({ path: `${process.env.OUT ?? '.'}/spike-b.png` });
  await a.screenshot({ path: `${process.env.OUT ?? '.'}/spike-a.png` });
} else {
  const c = await open('carol');
  console.log('C', JSON.stringify(await stats(c)));
}
await browser.close();

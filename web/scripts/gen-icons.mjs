// Renders web/icon/aenotes.svg (the source artwork: a rounded square with
// transparent corners) into the PNG favicons under web/public. The SVG is a
// traced bitmap of ~850 KB, too heavy to ship as the favicon itself.
// usage (from web/): node scripts/gen-icons.mjs
import { chromium } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const svg = readFileSync(resolve('icon/aenotes.svg'), 'utf8');
const sizes = { 'favicon-32.png': 32, 'apple-touch-icon.png': 180, 'icon-192.png': 192, 'icon-512.png': 512 };
const browser = await chromium.launch();
for (const [name, size] of Object.entries(sizes)) {
  const page = await browser.newPage({ viewport: { width: size, height: size } });
  const data = `data:image/svg+xml;base64,${Buffer.from(svg).toString('base64')}`;
  await page.setContent(
    `<html><body style="margin:0;background:transparent"><img src="${data}" style="width:${size}px;height:${size}px;object-fit:contain;display:block"></body></html>`,
  );
  await page.waitForFunction(() => document.querySelector('img')?.complete);
  await page.screenshot({ path: resolve('public', name), omitBackground: true });
  await page.close();
  console.log(name, size);
}
await browser.close();

// Folders on the start page: create, open, drag documents and folders
// between them (tiles and breadcrumbs), Move to…, search everywhere, delete.
// usage: node e2e/folders.mjs <baseURL> <outDir>
import { chromium } from '@playwright/test';

const [base, out = '.'] = process.argv.slice(2);
const api = (method, path, body) =>
  fetch(base + '/api/v1' + path, { method, headers: { 'Content-Type': 'application/json' }, body: body && JSON.stringify(body) }).then(r => (r.status === 204 ? null : r.json()));
const where = async id => (await api('GET', '/boards')).boards.find(b => b.id === id)?.folder;

const roadmap = await api('POST', '/boards', { title: 'Roadmap' });
const notes = await api('POST', '/boards', { title: 'Standup notes', mode: 'page' });

const browser = await chromium.launch();
const p = await (await browser.newContext({ viewport: { width: 1280, height: 860 } })).newPage();
p.on('pageerror', e => console.log('pageerror', e.message));
await p.goto(base + '/');
await p.waitForSelector('.board-card');

// New folder at the top level.
await p.getByRole('button', { name: 'New folder' }).click();
await p.getByPlaceholder('Folder name').fill('Work');
await p.getByRole('button', { name: 'Create folder' }).click();
await p.waitForSelector('.folder-card');
const work = (await api('GET', '/boards')).folders.find(f => f.name === 'Work');
console.log('folder created:', !!work);

// Drag a document onto the folder tile.
await p.locator('.board-card', { hasText: 'Roadmap' }).dragTo(p.locator('.folder-card', { hasText: 'Work' }));
await p.waitForTimeout(800);
console.log('Roadmap in Work after drag:', (await where(roadmap.id)) === work.id);
console.log('top level shows:', (await p.locator('.board-title').allTextContents()).join(', '));

// Into the folder: a subfolder and a page created there.
await p.locator('.folder-card', { hasText: 'Work' }).click();
await p.waitForURL(/\/f\//);
await p.getByRole('button', { name: 'New folder' }).click();
await p.getByPlaceholder('Folder name').fill('Meetings');
await p.getByRole('button', { name: 'Create folder' }).click();
await p.waitForSelector('.folder-card');
console.log('crumbs:', (await p.locator('.list-head .crumb').allTextContents()).join(' / '));
await p.screenshot({ path: `${out}/folders-work.png` });

// Drag the Roadmap card back up through the "All" breadcrumb.
await p.locator('.board-card', { hasText: 'Roadmap' }).dragTo(p.locator('.list-head .crumb', { hasText: 'All' }));
await p.waitForTimeout(800);
console.log('Roadmap back at the top via the breadcrumb:', (await where(roadmap.id)) === '');

// Move to… the notes page into Work/Meetings.
await p.goto(base + '/');
await p.waitForSelector('.board-card');
const card = p.locator('.board-card', { hasText: 'Standup notes' });
await card.hover();
await card.getByRole('button', { name: 'More' }).click();
await p.getByRole('menuitem', { name: 'Move to…' }).click();
await p.locator('.dialog .move-list button', { hasText: 'Meetings' }).click();
await p.waitForTimeout(800);
const meetings = (await api('GET', '/boards')).folders.find(f => f.name === 'Meetings');
console.log('notes moved into Meetings:', (await where(notes.id)) === meetings.id);

// Search finds it from the top, with its path.
await p.getByPlaceholder('Search everywhere…').fill('standup');
await p.waitForTimeout(300);
console.log('search result:', (await p.locator('.board-card').allInnerTexts()).map(t => t.replace(/\s+/g, ' ')).join(' | '));
await p.screenshot({ path: `${out}/folders-search.png` });
await p.getByPlaceholder('Search everywhere…').fill('');

// A folder cannot go into its own subfolder; a non-empty folder is not deleted.
const intoSelf = await api('PATCH', `/folders/${work.id}`, { parent: meetings.id });
console.log('Work into Meetings refused:', !!intoSelf.error);
const workCard = p.locator('.folder-card', { hasText: 'Work' });
await workCard.hover();
await workCard.getByRole('button', { name: 'More' }).click();
await p.getByRole('menuitem', { name: 'Delete' }).click();
await p.waitForTimeout(500);
console.log('deleting non-empty Work shows:', await p.locator('.error').innerText().catch(() => 'no error'));

// Open the page: its header shows the folder path.
await p.goto(`${base}/b/${notes.id}`);
await p.waitForSelector('affine-page-root');
console.log('page header path:', (await p.locator('.board-head .crumb').allTextContents()).join(' / '));
await p.screenshot({ path: `${out}/folders-page.png` });
await browser.close();

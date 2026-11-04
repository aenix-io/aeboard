// Generates internal/blocks/testdata/sample.bin: a page BlockSuite itself
// wrote, with one block of every kind the blocks package reads and writes
// (headings, text with marks and a link, lists, a todo, code, a quote, a
// divider, a reference to another document). The Go tests check their
// reading and their own blocks against it.
//   npx vite-node scripts/gen-blocks-sample.ts > ../internal/blocks/testdata/sample.bin
import { Text } from '@blocksuite/affine/store';
import { getTestStoreManager } from '@blocksuite/integration-test/store';
import * as Y from 'yjs';
import { DocsWorkspace } from '../src/board/workspace';

let n = 0;
const ws = new DocsWorkspace({ user: { name: 'x' }, create: async () => {}, idGenerator: () => `b${n++}` });
ws.storeExtensions = getTestStoreManager().get('store');
ws.setCatalog([{ id: 'doc', title: '', mode: 'page', created: new Date(0).toISOString() }]);
const live = ws.live('doc')!;
live.spaceDoc.clientID = 1;
const store = live.getStore();
store.load(() => {
  const root = store.addBlock('affine:page', { title: new Text('Sample') });
  store.addBlock('affine:surface', {}, root);
  const note = store.addBlock('affine:note', {}, root);
  store.addBlock('affine:paragraph', { type: 'h1', text: new Text('Heading') }, note);
  const pid = store.addBlock('affine:paragraph', { text: new Text('plain bold link') }, note);
  const t = (store.getBlock(pid)!.model as any).props.text;
  t.format(6, 4, { bold: true });
  t.format(11, 4, { link: 'https://example.com' });
  store.addBlock('affine:list', { type: 'bulleted', text: new Text('bullet') }, note);
  store.addBlock('affine:list', { type: 'numbered', text: new Text('number') }, note);
  store.addBlock('affine:list', { type: 'todo', checked: true, text: new Text('done') }, note);
  store.addBlock('affine:code', { language: 'go', text: new Text('fmt.Println(1)') }, note);
  store.addBlock('affine:paragraph', { type: 'quote', text: new Text('quoted') }, note);
  store.addBlock('affine:divider', {}, note);
  const rid = store.addBlock('affine:paragraph', { text: new Text('') }, note);
  (store.getBlock(rid)!.model as any).props.text.insert(' ', 0, { reference: { type: 'LinkedPage', pageId: 'abcdefghjk' } });
});
const json = (live.spaceDoc.getMap('blocks') as Y.Map<unknown>).toJSON();
const deltas: Record<string, unknown> = {};
(live.spaceDoc.getMap('blocks') as Y.Map<Y.Map<unknown>>).forEach((b, id) => {
  const t = b.get('prop:text');
  if (t instanceof Y.Text) deltas[id] = t.toDelta();
});
const types: Record<string, Record<string, string>> = {};
const kind = (v: unknown): string => v instanceof Y.Text ? 'Y.Text' : v instanceof Y.Array ? 'Y.Array' : v instanceof Y.Map ? `Y.Map{${[...(v as Y.Map<unknown>).entries()].map(([k, x]) => k + ':' + kind(x)).join(',')}}` : typeof v;
(live.spaceDoc.getMap('blocks') as Y.Map<Y.Map<unknown>>).forEach((b, id) => {
  types[id] = {};
  b.forEach((v, k) => { types[id][k] = kind(v); });
});
process.stdout.write(Y.encodeStateAsUpdate(live.spaceDoc), () => process.exit(0));

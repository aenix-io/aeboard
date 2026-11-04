// Generates the seed every new board starts from: the BlockSuite root blocks
// (affine:page + affine:surface) as one Yjs V1 update.
//
// Why a constant: two browsers opening a brand-new board at the same moment
// would each see an empty doc and each create a root — two affine:page blocks.
// Instead the server hands out this fixed update for any board with no stored
// state. It is written by clientID 0 with fixed block ids, so applying it
// twice (or merging two replicas that both started from it) is a no-op —
// identical Yjs items, not duplicates.
//
// A page additionally holds one note with an empty paragraph, the place the
// text of a page goes.
//
// Regenerate (only when BlockSuite's schema changes):
//   npx vite-node scripts/gen-seed.ts board > ../internal/seed/seed.bin
//   npx vite-node scripts/gen-seed.ts page  > ../internal/seed/page.bin
import { Text } from '@blocksuite/affine/store';
import { getTestStoreManager } from '@blocksuite/integration-test/store';
import * as Y from 'yjs';

import { DocsWorkspace } from '../src/board/workspace';

let n = 0;
// No syncURL: the document is built offline.
const ws = new DocsWorkspace({ user: { name: 'seed' }, create: async () => {}, idGenerator: () => `seed-${n++}` });
ws.storeExtensions = getTestStoreManager().get('store');
ws.setCatalog([{ id: 'seed', title: '', mode: 'edgeless', created: new Date(0).toISOString() }]);
const live = ws.live('seed')!;
const doc = live.spaceDoc;
doc.clientID = 0;
const store = live.getStore();
const kind = process.argv[2] ?? 'board';
if (kind !== 'board' && kind !== 'page') throw new Error(`unknown seed kind ${kind}`);
store.load(() => {
  const rootId = store.addBlock('affine:page', { title: new Text() }, undefined);
  store.addBlock('affine:surface', {}, rootId);
  if (kind === 'page') {
    const noteId = store.addBlock('affine:note', {}, rootId);
    store.addBlock('affine:paragraph', {}, noteId);
  }
});
// BlockSuite keeps timers alive (blob sync), so exit explicitly once written.
process.stdout.write(Y.encodeStateAsUpdate(doc), () => process.exit(0));

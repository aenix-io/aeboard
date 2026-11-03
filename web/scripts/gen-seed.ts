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
// Regenerate (only when BlockSuite's page/surface schema changes):
//   npx vite-node scripts/gen-seed.ts > ../internal/seed/seed.bin
import { Text } from '@blocksuite/affine/store';
import { getTestStoreManager } from '@blocksuite/integration-test/store';
import { Awareness } from 'y-protocols/awareness.js';
import * as Y from 'yjs';

import { BoardWorkspace } from '../src/board/workspace';

const doc = new Y.Doc({ guid: 'seed' });
doc.clientID = 0;
let n = 0;
const ws = new BoardWorkspace({
  id: 'seed',
  spaceDoc: doc,
  awareness: new Awareness(new Y.Doc()),
  idGenerator: () => `seed-${n++}`,
});
ws.storeExtensions = getTestStoreManager().get('store');
const store = ws.getDoc('seed')!.getStore();
store.load(() => {
  const rootId = store.addBlock('affine:page', { title: new Text() }, undefined);
  store.addBlock('affine:surface', {}, rootId);
});
// BlockSuite keeps timers alive (blob sync), so exit explicitly once written.
process.stdout.write(Y.encodeStateAsUpdate(doc), () => process.exit(0));

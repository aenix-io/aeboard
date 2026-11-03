// Mounts a BlockSuite edgeless editor over one board synced through the
// aeboard server (a y-websocket room per board).
import '@blocksuite/affine/effects';
import '@toeverything/theme/style.css';

import { TestAffineEditorContainer } from '@blocksuite/integration-test';
import { getTestStoreManager } from '@blocksuite/integration-test/store';
import { getTestViewManager } from '@blocksuite/integration-test/view';
import { Text } from '@blocksuite/affine/store';
import { WebsocketProvider } from 'y-websocket';
import * as Y from 'yjs';

import { BoardWorkspace } from './workspace';

let defined = false;
function defineElements() {
  if (defined) return;
  defined = true;
  customElements.define('affine-editor-container', TestAffineEditorContainer);
}

export interface BoardSession {
  editor: TestAffineEditorContainer;
  provider: WebsocketProvider;
  doc: Y.Doc;
  destroy(): void;
}

export interface User {
  name: string;
}

// openBoard connects to the board's room, waits for the first sync (so an
// existing board is never re-initialised), creates the root blocks only when
// the synced document is still empty, and mounts the editor into `mount`.
export async function openBoard(boardId: string, user: User, mount: HTMLElement): Promise<BoardSession> {
  defineElements();
  const doc = new Y.Doc({ guid: boardId });
  const wsBase = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/sync`;
  const provider = new WebsocketProvider(wsBase, boardId, doc);
  // BlockSuite reads selectionV2 off every peer's awareness state without a
  // null check, so a peer must never publish a state without it.
  provider.awareness.setLocalState({ user: { name: user.name }, selectionV2: {} });

  await new Promise<void>(resolve => {
    if (provider.synced) return resolve();
    const onSync = (synced: boolean) => {
      if (!synced) return;
      provider.off('sync', onSync);
      resolve();
    };
    provider.on('sync', onSync);
  });

  const ws = new BoardWorkspace({ id: boardId, spaceDoc: doc, awareness: provider.awareness });
  ws.storeExtensions = getTestStoreManager().get('store');
  const bdoc = ws.getDoc(boardId)!;
  const store = bdoc.getStore();
  // store.load (not doc.load): it also runs the store extensions' loaded()
  // hooks — selection among them, which is what subscribes to remote
  // awareness. doc.load alone leaves remote cursors/selections dark.
  store.load(() => {
    if (store.root) return;
    const rootId = store.addBlock('affine:page', { title: new Text() });
    store.addBlock('affine:surface', {}, rootId);
  });
  store.resetHistory();

  const editor = new TestAffineEditorContainer();
  editor.doc = store;
  editor.pageSpecs = getTestViewManager().get('page');
  editor.edgelessSpecs = getTestViewManager().get('edgeless');
  editor.mode = 'edgeless';
  mount.replaceChildren(editor);

  return {
    editor,
    provider,
    doc,
    destroy() {
      editor.remove();
      provider.destroy();
      ws.dispose();
      doc.destroy();
    },
  };
}

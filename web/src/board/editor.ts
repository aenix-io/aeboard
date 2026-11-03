// Mounts a BlockSuite edgeless editor over one board synced through the
// aeboard server (a y-websocket room per board).
import '@blocksuite/affine/effects';
import '@toeverything/theme/style.css';

import { TestAffineEditorContainer } from '@blocksuite/integration-test';
import { getTestStoreManager } from '@blocksuite/integration-test/store';
import { getTestViewManager } from '@blocksuite/integration-test/view';
import { DocModeExtension, type DocModeProvider } from '@blocksuite/affine/shared/services';
import { Subject } from 'rxjs';
import { WebsocketProvider } from 'y-websocket';
import * as Y from 'yjs';

import { ServerBlobSource } from './blobs';
import { BoardWorkspace } from './workspace';

// A board is always edgeless. BlockSuite's default DocModeProvider answers
// "page", and the selection toolbar (fill/border colours, text, connectors,
// lock…) only shows for surface elements in edgeless mode, so without this
// override selecting a shape shows no toolbar at all.
const edgelessOnly: DocModeProvider = {
  getEditorMode: () => 'edgeless',
  setEditorMode: () => {},
  getPrimaryMode: () => 'edgeless',
  setPrimaryMode: () => {},
  togglePrimaryMode: () => 'edgeless',
  onPrimaryModeChange: () => new Subject<never>().subscribe(),
};

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

// openBoard connects to the board's room, waits for the first sync and mounts
// the editor into `mount`.
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

  const ws = new BoardWorkspace({
    id: boardId,
    spaceDoc: doc,
    awareness: provider.awareness,
    blobSource: new ServerBlobSource(boardId),
  });
  ws.storeExtensions = getTestStoreManager().get('store');
  const bdoc = ws.getDoc(boardId)!;
  const store = bdoc.getStore();
  // store.load (not doc.load): it also runs the store extensions' loaded()
  // hooks — selection among them, which is what subscribes to remote
  // awareness. doc.load alone leaves remote cursors/selections dark.
  store.load();
  // The root blocks come from the server: every board starts from the seed
  // state (internal/seed), so the client never creates them — two clients
  // doing that at once would leave the board with two roots.
  if (!store.root) {
    provider.destroy();
    throw new Error('the board has no root block (was it created through the server?)');
  }
  store.resetHistory();

  const editor = new TestAffineEditorContainer();
  editor.doc = store;
  editor.pageSpecs = getTestViewManager().get('page');
  editor.edgelessSpecs = [...getTestViewManager().get('edgeless'), DocModeExtension(edgelessOnly)];
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

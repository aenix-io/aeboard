// Mounts the BlockSuite editor over one aenotes document — a board
// (edgeless) or a page — synced through the server, one room per document.
import '@blocksuite/affine/effects';
import '@toeverything/theme/style.css';

import { TestAffineEditorContainer } from '@blocksuite/integration-test';
import { getTestStoreManager } from '@blocksuite/integration-test/store';
import { getTestViewManager } from '@blocksuite/integration-test/view';
import { RefNodeSlotsProvider } from '@blocksuite/affine/inlines/reference';
import { DocModeExtension, type DocModeProvider } from '@blocksuite/affine/shared/services';
import type { DocMode } from '@blocksuite/affine/model';
import { Subject } from 'rxjs';

import { api } from '../api';
import { ServerBlobSource } from './blobs';
import { ToastNotifications } from './notifications';
import { registerTemplates } from './templates';
import { type CatalogEntry, DocsWorkspace, type LiveDoc } from './workspace';

let defined = false;
function defineElements() {
  if (defined) return;
  defined = true;
  registerTemplates();
  customElements.define('affine-editor-container', TestAffineEditorContainer);
}

export interface User {
  name: string;
}

export interface OpenOptions {
  /** A visitor who may only look: the editor takes no input (the server
   *  also drops whatever a read-only socket sends). */
  readonly?: boolean;
  /** The view to open in; the document's own mode when omitted. */
  mode?: DocMode;
  /** Called when the visitor follows a link to another document. */
  onNavigate?: (id: string, mode?: DocMode) => void;
}

export interface DocSession {
  editor: TestAffineEditorContainer;
  doc: LiveDoc;
  workspace: DocsWorkspace;
  /** Switch between the board and the page view of the same document. */
  setMode(mode: DocMode): void;
  destroy(): void;
}

// One workspace per page load: it holds every document's Y.Doc, so going
// from a page to the board it links to reuses what is already synced.
let workspace: DocsWorkspace | null = null;
let workspaceUser = '';

function syncURL() {
  return `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/sync`;
}

/** getWorkspace returns the session's workspace (a new one if the visitor's
 *  name changed, since every room's presence carries it). */
export function getWorkspace(user: User): DocsWorkspace {
  if (workspace && workspaceUser === user.name) return workspace;
  workspace?.dispose();
  workspaceUser = user.name;
  workspace = new DocsWorkspace({
    user,
    syncURL: syncURL(),
    // Attachments are addressed per document on the server but stored by
    // content, so any document's route serves them; the workspace uses the
    // one of whichever document asks.
    blobSource: new ServerBlobSource(() => currentDocID),
    exists: async id => {
      try {
        return (await fetch(`/api/v1/boards/${id}`, { credentials: 'same-origin' })).status !== 404;
      } catch {
        return true; // offline: no answer is not a no
      }
    },
    create: async id => {
      // The editor makes a document from inside the open one ("@" → new
      // doc, "turn into linked doc"): it lives there.
      await api.createBoard({ id, mode: 'page', blank: true, parent: currentDocID || undefined });
    },
  });
  workspace.storeExtensions = getTestStoreManager().get('store');
  return workspace;
}

let currentDocID = '';

/** refreshCatalog loads the document list the link menu offers. */
export async function refreshCatalog(ws: DocsWorkspace): Promise<void> {
  const list = await api.listBoards();
  ws.setCatalog(list.boards as CatalogEntry[]);
}

// openDoc joins the document's room, waits for the first sync and mounts
// the editor into `mount`.
export async function openDoc(id: string, user: User, mount: HTMLElement, opts: OpenOptions = {}): Promise<DocSession> {
  defineElements();
  const ws = getWorkspace(user);
  await refreshCatalog(ws);
  const doc = ws.live(id);
  if (!doc) throw new Error('document not found');
  currentDocID = id;
  doc.load();
  await doc.whenSynced();

  const store = doc.getStore();
  // store.load (not doc.load): it also runs the store extensions' loaded()
  // hooks — selection among them, which is what subscribes to remote
  // awareness. doc.load alone leaves remote cursors/selections dark.
  store.load();
  // The root blocks come from the server: every document starts from its
  // mode's seed state (internal/seed), so the client never creates them —
  // two clients doing that at once would leave it with two roots.
  if (!store.root) throw new Error('the document has no root block (was it created through the server?)');
  store.resetHistory();
  if (opts.readonly) store.readonly = true;

  let mode: DocMode = opts.mode ?? (ws.modeOf(id) === 'page' ? 'page' : 'edgeless');
  const modeChanged = new Subject<DocMode>();
  // BlockSuite asks this which view is on screen (the element toolbar only
  // shows in edgeless) and what a linked document opens as.
  const modes: DocModeProvider = {
    getEditorMode: () => mode,
    setEditorMode: m => {
      mode = m;
    },
    getPrimaryMode: docId => (ws.modeOf(docId) === 'page' ? 'page' : 'edgeless'),
    setPrimaryMode: () => {},
    togglePrimaryMode: docId => (ws.modeOf(docId) === 'page' ? 'page' : 'edgeless'),
    onPrimaryModeChange: (handler, _docId) => modeChanged.subscribe(handler),
  };

  const editor = new TestAffineEditorContainer();
  editor.doc = store;
  editor.pageSpecs = [...getTestViewManager().get('page'), DocModeExtension(modes), ToastNotifications];
  editor.edgelessSpecs = [...getTestViewManager().get('edgeless'), DocModeExtension(modes), ToastNotifications];
  editor.mode = mode;
  mount.replaceChildren(editor);

  // Links to other documents (references, linked cards) open them here.
  const sub = editor.std.get(RefNodeSlotsProvider).docLinkClicked.subscribe(e => {
    if (e.pageId === id) return;
    opts.onNavigate?.(e.pageId, e.params?.mode);
  });

  return {
    editor,
    doc,
    workspace: ws,
    setMode(m) {
      mode = m;
      editor.switchEditor(m);
    },
    destroy() {
      sub.unsubscribe();
      editor.remove();
      // Leave the room (presence) but keep the Y.Doc: coming back, or a page
      // embedding it, re-syncs from what is here.
      doc.disconnect();
    },
  };
}

// A BlockSuite Workspace over every aenotes document the visitor can see.
//
// BlockSuite's reference TestWorkspace keeps every document as a Yjs
// *subdocument* of one root doc. The y-websocket protocol syncs one Y.Doc
// per connection and never carries subdocument content, so here each
// document is a standalone Y.Doc (guid = document id) with its own room on
// the server and its own awareness (cursors), connected only when the
// document is loaded: the one being edited, and any other one a page embeds.
//
// The workspace's own root doc only holds the document list (meta) that the
// link menu, references and embeds read; it stays local and is filled from
// the server's catalog — the catalog, not this doc, is the list's truth.
import {
  AwarenessStore,
  type Doc,
  type ExtensionType,
  type GetStoreOptions,
  type IdGenerator,
  nanoid,
  type RemoveStoreOptions,
  type Store,
  StoreContainer,
  type Workspace,
  type YBlock,
} from '@blocksuite/affine/store';
import { TestMeta } from '@blocksuite/affine/store/test';
import { NoopLogger } from '@blocksuite/affine/global/utils';
import { BlobEngine, type BlobSource, MemoryBlobSource } from '@blocksuite/affine/sync';
import { Subject } from 'rxjs';
import { Awareness } from 'y-protocols/awareness.js';
import { WebsocketProvider } from 'y-websocket';
import * as Y from 'yjs';

/** A catalog entry, as the server lists it. */
export interface CatalogEntry {
  id: string;
  title: string;
  mode: 'edgeless' | 'page';
  created: string;
}

export interface WorkspaceOptions {
  user: { name: string };
  /** Attachments of every document go through this source. */
  blobSource?: BlobSource;
  /** Creates a document on the server (a page made from the link menu); the
   *  document's room is not joined before it resolves. */
  create: (id: string) => Promise<void>;
  /** Where the sync rooms live; without it documents never connect (the
   *  seed generator builds documents offline). */
  syncURL?: string;
  /** Block id generator (fixed in the seed generator). */
  idGenerator?: IdGenerator;
}

const idAlphabet = '0123456789abcdefghjkmnpqrstvwxyz';

/** newDocID mints an id in the server's shape (10 Crockford base32 chars). */
export function newDocID(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(10));
  return Array.from(bytes, b => idAlphabet[b % idAlphabet.length]).join('');
}

export class DocsWorkspace implements Workspace {
  readonly id = 'aenotes';
  readonly meta: TestMeta;
  readonly idGenerator: IdGenerator;
  readonly blobSync: BlobEngine;
  readonly slots = { docListUpdated: new Subject<void>() };
  storeExtensions: ExtensionType[] = [];

  private readonly _rootDoc = new Y.Doc({ guid: 'aenotes:workspace' });
  private readonly _docs = new Map<string, LiveDoc>();
  private readonly _modes = new Map<string, CatalogEntry['mode']>();

  constructor(readonly opts: WorkspaceOptions) {
    this.idGenerator = opts.idGenerator ?? nanoid;
    this.meta = new TestMeta(this._rootDoc);
    this.meta.initialize();
    this.blobSync = new BlobEngine(opts.blobSource ?? new MemoryBlobSource(), [], new NoopLogger());
    this.blobSync.start();
    this.meta.docMetaUpdated.subscribe(() => this.slots.docListUpdated.next());
  }

  get doc(): Y.Doc {
    return this._rootDoc;
  }

  get docs(): Map<string, Doc> {
    for (const m of this.meta.docMetas) this._entry(m.id);
    return this._docs as Map<string, Doc>;
  }

  /** modeOf is a document's primary mode (board unless the catalog says page). */
  modeOf(id: string): CatalogEntry['mode'] {
    return this._modes.get(id) ?? 'edgeless';
  }

  /** setCatalog brings the document list in line with the server's. */
  setCatalog(entries: CatalogEntry[]): void {
    const seen = new Set<string>();
    for (const e of entries) {
      seen.add(e.id);
      this._modes.set(e.id, e.mode);
      const cur = this.meta.getDocMeta(e.id);
      if (!cur) {
        this.meta.addDocMeta({ id: e.id, title: e.title, createDate: Date.parse(e.created), tags: [] });
      } else if (cur.title !== e.title && !this._docs.get(e.id)?.loaded) {
        // A loaded document's title is live in its own state; only the
        // others take the catalog's.
        this.meta.setDocMeta(e.id, { title: e.title });
      }
    }
    for (const m of [...this.meta.docMetas]) {
      if (!seen.has(m.id) && !this._docs.get(m.id)?.creating) this.meta.removeDocMeta(m.id);
    }
  }

  private _entry(id: string): LiveDoc | null {
    let d = this._docs.get(id);
    if (!d && this.meta.getDocMeta(id)) {
      d = new LiveDoc(this, id);
      this._docs.set(id, d);
    }
    return d ?? null;
  }

  createDoc(docId?: string): Doc {
    const id = docId ?? newDocID();
    this.meta.addDocMeta({ id, title: '', createDate: Date.now(), tags: [] });
    this._modes.set(id, 'page');
    const d = new LiveDoc(this, id, this.opts.create(id));
    this._docs.set(id, d);
    return d;
  }

  getDoc(docId: string): Doc | null {
    return this._entry(docId);
  }

  /** live is getDoc with aenotes's own surface (sync, provider). */
  live(docId: string): LiveDoc | null {
    return this._entry(docId);
  }

  removeDoc(): void {
    throw new Error('aenotes: documents are not deleted from the editor');
  }

  dispose(): void {
    for (const d of this._docs.values()) d.disconnect();
    this.blobSync.stop();
  }
}

/** One document: its Y.Doc, and — once loaded — its room on the server. */
export class LiveDoc implements Doc {
  readonly id: string;
  readonly awarenessStore: AwarenessStore;
  private readonly _spaceDoc: Y.Doc;
  private readonly _container: StoreContainer;
  private _provider: WebsocketProvider | null = null;
  private _ready = false;
  private _synced = false;
  private _syncWaiters: Array<() => void> = [];

  constructor(
    private readonly _ws: DocsWorkspace,
    id: string,
    /** Set while the server creates this document (see createDoc). */
    private _creating?: Promise<void>,
  ) {
    this.id = id;
    this._spaceDoc = new Y.Doc({ guid: id });
    const awareness = new Awareness(this._spaceDoc);
    // BlockSuite reads selectionV2 off every peer's awareness state without
    // a null check, so a peer must never publish a state without it.
    awareness.setLocalState({ user: { name: _ws.opts.user.name }, selectionV2: {} });
    this.awarenessStore = new AwarenessStore(awareness);
    this._container = new StoreContainer(this);
    // Keep the list's title in step with the document's own.
    this._spaceDoc.on('update', () => this._syncTitle());
  }

  get creating(): boolean {
    return this._creating !== undefined;
  }

  get meta() {
    return this._ws.meta.getDocMeta(this.id);
  }
  get ready() {
    return this._ready;
  }
  /** loaded: the first sync with the server is done. */
  get loaded() {
    return this._synced;
  }
  get workspace() {
    return this._ws;
  }
  get rootDoc() {
    return this._ws.doc;
  }
  get spaceDoc() {
    return this._spaceDoc;
  }
  get yBlocks(): Y.Map<YBlock> {
    return this._spaceDoc.getMap('blocks') as Y.Map<YBlock>;
  }

  /** load joins the document's room (once); initFn runs at once, on the
   *  local doc — BlockSuite's "new doc" path builds the blocks there. */
  load(initFn?: () => void): void {
    if (!this._ready) {
      initFn?.();
      this._ready = true;
    }
    const url = this._ws.opts.syncURL;
    if (this._provider || !url) return;
    const connect = () => {
      this._provider = new WebsocketProvider(url, this.id, this._spaceDoc, {
        awareness: this.awarenessStore.awareness,
      });
      this._provider.on('sync', (synced: boolean) => {
        if (!synced || this._synced) return;
        this._synced = true;
        this._syncWaiters.splice(0).forEach(f => f());
      });
    };
    if (this._creating) {
      void this._creating.then(
        () => {
          this._creating = undefined;
          connect();
        },
        err => console.error(`aenotes: could not create document ${this.id}`, err),
      );
    } else {
      connect();
    }
  }

  /** whenSynced resolves after the first sync with the server. */
  whenSynced(): Promise<void> {
    if (this._synced) return Promise.resolve();
    return new Promise(resolve => this._syncWaiters.push(resolve));
  }

  get provider(): WebsocketProvider | null {
    return this._provider;
  }

  disconnect(): void {
    this._provider?.destroy();
    this._provider = null;
    this._synced = false;
  }

  private _syncTitle() {
    const root = [...this.yBlocks.values()].find(b => b.get('sys:flavour') === 'affine:page');
    const t = root?.get('prop:title');
    const title = t instanceof Y.Text ? t.toString() : undefined;
    if (title !== undefined && this.meta && this.meta.title !== title) {
      this._ws.meta.setDocMeta(this.id, { title });
    }
  }

  getStore(options: GetStoreOptions = {}): Store {
    // Same store-id rule as BlockSuite's TestDoc: the default store is keyed by
    // the space doc guid, readonly/query variants get their own key.
    const id =
      options.id ?? (options.readonly !== undefined || options.query ? undefined : this._spaceDoc.guid);
    return this._container.getStore({
      ...options,
      id,
      extensions: this._ws.storeExtensions.concat(options.extensions ?? []),
    });
  }

  removeStore = (options: RemoveStoreOptions) => this._container.removeStore(options);

  remove(): void {
    throw new Error('aenotes: documents are not deleted from the editor');
  }

  dispose(): void {}

  clear(): void {
    this.yBlocks.clear();
  }
}

// A BlockSuite Workspace that holds exactly one board.
//
// BlockSuite's reference TestWorkspace keeps every document as a Yjs
// *subdocument* of a root doc (rootDoc.getMap('spaces')). The y-websocket
// protocol syncs one Y.Doc per connection and never carries subdocument
// content, so that layout would need a second provider per subdoc plus a
// synced root doc that only holds a page list.
//
// aeboard does not need the page list: a board is one edgeless document. So
// the space doc here is a standalone Y.Doc (guid = board id) and is the only
// thing synced — one board = one Y.Doc = one ygo room. The root doc exists
// only because the Workspace/Doc interfaces expose it (doc metadata such as
// the title); it stays local and is never sent anywhere. The board list and
// titles live on the server.
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
import { BlobEngine, type BlobSource, MemoryBlobSource } from '@blocksuite/affine/sync';
import { NoopLogger } from '@blocksuite/affine/global/utils';
import { Subject } from 'rxjs';
import { Awareness } from 'y-protocols/awareness.js';
import * as Y from 'yjs';

export class BoardWorkspace implements Workspace {
  readonly id: string;
  readonly meta: TestMeta;
  readonly idGenerator: IdGenerator;
  readonly blobSync: BlobEngine;
  readonly awarenessStore: AwarenessStore;
  readonly slots = { docListUpdated: new Subject<void>() };
  storeExtensions: ExtensionType[] = [];

  private readonly _rootDoc: Y.Doc;
  private readonly _docs = new Map<string, Doc>();

  constructor(opts: {
    id: string;
    spaceDoc: Y.Doc;
    awareness: Awareness;
    blobSource?: BlobSource;
    idGenerator?: IdGenerator;
  }) {
    this.id = opts.id;
    this.idGenerator = opts.idGenerator ?? nanoid;
    this._rootDoc = new Y.Doc({ guid: `${opts.id}:root` });
    this.meta = new TestMeta(this._rootDoc);
    this.meta.initialize();
    this.awarenessStore = new AwarenessStore(opts.awareness);
    this.blobSync = new BlobEngine(opts.blobSource ?? new MemoryBlobSource(), [], new NoopLogger());
    this.blobSync.start();
    this.meta.addDocMeta({ id: opts.spaceDoc.guid, title: '', createDate: Date.now(), tags: [] });
    this._docs.set(opts.spaceDoc.guid, new BoardDoc(this, this._rootDoc, opts.spaceDoc));
  }

  get doc(): Y.Doc {
    return this._rootDoc;
  }

  get docs(): Map<string, Doc> {
    return this._docs;
  }

  createDoc(): Doc {
    throw new Error('aeboard: a board workspace holds exactly one doc');
  }

  getDoc(docId: string): Doc | null {
    return this._docs.get(docId) ?? null;
  }

  removeDoc(): void {
    throw new Error('aeboard: a board workspace holds exactly one doc');
  }

  dispose(): void {
    this.blobSync.stop();
    this.awarenessStore.destroy();
  }
}

class BoardDoc implements Doc {
  readonly id: string;
  private readonly _container: StoreContainer;
  private _ready = false;

  constructor(
    private readonly _ws: BoardWorkspace,
    private readonly _rootDoc: Y.Doc,
    private readonly _spaceDoc: Y.Doc,
  ) {
    this.id = _spaceDoc.guid;
    this._container = new StoreContainer(this);
  }

  get meta() {
    return this._ws.meta.getDocMeta(this.id);
  }
  get ready() {
    return this._ready;
  }
  get loaded() {
    return true;
  }
  get awarenessStore() {
    return this._ws.awarenessStore;
  }
  get workspace() {
    return this._ws;
  }
  get rootDoc() {
    return this._rootDoc;
  }
  get spaceDoc() {
    return this._spaceDoc;
  }
  get yBlocks(): Y.Map<YBlock> {
    return this._spaceDoc.getMap('blocks') as Y.Map<YBlock>;
  }

  load(initFn?: () => void): void {
    if (this._ready) return;
    initFn?.();
    this._ready = true;
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
    throw new Error('aeboard: a board doc cannot be removed from its workspace');
  }

  dispose(): void {}

  clear(): void {
    this.yBlocks.clear();
  }
}

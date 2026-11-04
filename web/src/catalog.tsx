// The document catalog — documents and folders — shared by the start page
// and the sidebar, so a change made in one shows in the other at once.
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { api, type Board, type Folder } from './api';
import { atFolderLevel, canMoveFolder, type Crumb, inside, placeAmong, placeAt, type Placement } from './library';

export type Mode = 'edgeless' | 'page';

/** What is being dragged: a folder, a document, or a shortcut to one (a
 *  link from another document). from is the document it is dragged out of:
 *  its parent, or for a shortcut the document that links to it. */
export type Dragged = { kind: 'doc' | 'folder'; id: string; from?: string; shortcut?: boolean };

export const DRAG_TYPE = 'application/x-aenotes-item';

export function dragData(e: React.DragEvent): Dragged | null {
  try {
    const v = JSON.parse(e.dataTransfer.getData(DRAG_TYPE)) as Dragged;
    if (!v || (v.kind !== 'doc' && v.kind !== 'folder') || typeof v.id !== 'string') return null;
    return {
      kind: v.kind,
      id: v.id,
      from: typeof v.from === 'string' ? v.from : undefined,
      shortcut: v.shortcut === true,
    };
  } catch {
    return null;
  }
}

/** Trail is the way the visitor came to the open document in the sidebar:
 *  the row they clicked (node) and the folders and documents above it. */
export interface Trail {
  doc: string;
  node: string;
  path: Crumb[];
  /** The row was a shortcut, not the document's own place. */
  shortcut: boolean;
}

/** Where a link goes among a document's shortcuts. */
export type LinkPlace = { before?: string; after?: string; first?: boolean };

export interface Catalog {
  /** The documents, those in the trash left out. */
  docs: Board[] | null;
  /** The documents in the trash, most recently trashed first. */
  trash: Board[];
  folders: Folder[];
  error: string | null;
  canWrite: boolean;
  reload(): void;
  /** patchDoc updates a document in place (a title typed in the editor). */
  patchDoc(id: string, patch: Partial<Board>): void;
  trail: Trail | null;
  setTrail(t: Trail | null): void;
  /** accepts reports whether item may be dropped into folder target. */
  accepts(item: Dragged | null, target: string): boolean;
  move(item: Dragged, target: string): Promise<void>;
  /** place puts item at `index` of folder `parent`'s entries (counted
   *  without the item): the order people set by dragging. */
  place(item: Dragged, parent: string, index: number): Promise<void>;
  /** placeInDoc moves a document to live inside parent, at `index` of the
   *  documents living there. */
  placeInDoc(item: Dragged, parent: string, index: number): Promise<void>;
  /** placeShortcut moves a shortcut (a link) into parent, among its
   *  shortcuts. */
  placeShortcut(item: Dragged, parent: string, at: LinkPlace): Promise<void>;
  createDoc(mode: Mode, folder: string, title?: string): Promise<Board>;
  /** createChild makes a new document inside parent (and links it there). */
  createChild(mode: Mode, parent: string): Promise<Board>;
  /** makeOriginal moves a document to where its shortcut in parent is; where
   *  it lived (a document) keeps a shortcut to it instead. */
  makeOriginal(doc: string, parent: string): Promise<void>;
  /** copyShortcut turns the shortcut in parent into a separate copy. */
  copyShortcut(doc: string, parent: string): Promise<Board>;
  /** removeShortcut takes the shortcut in parent away (its link line). */
  removeShortcut(doc: string, parent: string): Promise<void>;
  createFolder(name: string, parent: string): Promise<Folder>;
  renameFolder(id: string, name: string): Promise<void>;
  deleteFolder(id: string): Promise<void>;
  /** trashDoc moves a document to the trash, restoreDoc brings it back. */
  trashDoc(id: string): Promise<void>;
  restoreDoc(id: string): Promise<void>;
  /** deleteDoc deletes a trashed document for good. */
  deleteDoc(id: string): Promise<void>;
}

const Ctx = createContext<Catalog | null>(null);

export function useCatalog(): Catalog {
  const c = useContext(Ctx);
  if (!c) throw new Error('useCatalog outside CatalogProvider');
  return c;
}

/** A safety net: the catalog also refreshes on its own this often, in case
 *  the status stream (which announces every change) is down. */
const REFRESH_MS = 30_000;

/** unlinkQuietly takes doc's link line out of parent, where a mention in the
 *  text may keep it: then it stays there as a shortcut, which is right. */
async function unlinkQuietly(parent: string, doc: string) {
  try {
    await api.removeChild(parent, doc);
  } catch {
    /* mentioned in the text there: it stays a shortcut */
  }
}

export function CatalogProvider({
  canWrite,
  version,
  children,
}: {
  canWrite: boolean;
  /** The server's catalog version from the status stream: when it moves,
   *  someone changed the structure, and the list is fetched again — so it
   *  updates for everyone without a reload. */
  version?: number;
  children: ReactNode;
}) {
  // Every document, the trashed ones included; split below.
  const [all, setDocs] = useState<Board[] | null>(null);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [trail, setTrail] = useState<Trail | null>(null);
  const seq = useRef(0);

  const reload = useCallback(() => {
    const n = ++seq.current;
    api.listBoards().then(
      l => {
        if (n !== seq.current) return; // a newer reload already answered
        setDocs(l.boards);
        setFolders(l.folders ?? []);
        setError(null);
      },
      e => setError(String((e as Error).message ?? e)),
    );
  }, []);

  useEffect(() => {
    reload();
    const t = setInterval(reload, REFRESH_MS);
    window.addEventListener('focus', reload);
    return () => {
      clearInterval(t);
      window.removeEventListener('focus', reload);
    };
  }, [reload]);

  useEffect(() => {
    if (version !== undefined) reload();
  }, [version, reload]);

  const docs = useMemo(() => all?.filter(d => !d.trashed) ?? null, [all]);
  const trash = useMemo(
    () => (all ?? []).filter(d => d.trashed).sort((a, b) => (b.trashed ?? '').localeCompare(a.trashed ?? '')),
    [all],
  );

  const value = useMemo<Catalog>(() => {
    const act = async <T,>(f: () => Promise<T>): Promise<T> => {
      try {
        setError(null);
        return await f();
      } catch (e) {
        setError(String((e as Error).message ?? e));
        throw e;
      } finally {
        reload();
      }
    };
    const patchLocal = (id: string, patch: Partial<Board>) =>
      setDocs(ds => ds?.map(d => (d.id === id ? { ...d, ...patch } : d)) ?? ds);
    const setTrashed = (id: string, on: boolean) =>
      act(async () => {
        // Show it at once; the reload confirms it.
        patchLocal(id, { trashed: on ? new Date().toISOString() : undefined });
        await api.updateBoard(id, { trashed: on });
      });
    // writeRanks stores new ranks; the moving item also gets its new home.
    const writeRanks = async (ps: Placement[], item: Dragged, home: { folder?: string; parent?: string }) => {
      const rankOf = (kind: string, id: string) => ps.find(p => p.kind === kind && p.id === id)?.rank;
      // Show the new order at once; the reload confirms it.
      setDocs(
        ds =>
          ds?.map(d => {
            const r = rankOf('doc', d.id);
            if (item.kind === 'doc' && d.id === item.id)
              return { ...d, folder: home.folder ?? '', parent: home.parent, rank: r ?? d.rank };
            return r ? { ...d, rank: r } : d;
          }) ?? ds,
      );
      setFolders(fs =>
        fs.map(f => {
          const r = rankOf('folder', f.id);
          if (item.kind === 'folder' && f.id === item.id) return { ...f, parent: home.folder ?? '', rank: r ?? f.rank };
          return r ? { ...f, rank: r } : f;
        }),
      );
      for (const p of ps) {
        const moving = p.kind === item.kind && p.id === item.id;
        if (p.kind === 'doc') {
          const where = home.parent ? { parent: home.parent } : { folder: home.folder ?? '' };
          await api.updateBoard(p.id, moving ? { ...where, rank: p.rank } : { rank: p.rank });
        } else {
          await api.updateFolder(p.id, moving ? { parent: home.folder ?? '', rank: p.rank } : { rank: p.rank });
        }
      }
      // A rank that did not change still moves the item.
      if (!ps.some(p => p.kind === item.kind && p.id === item.id)) {
        if (item.kind === 'doc') await api.updateBoard(item.id, home.parent ? { parent: home.parent } : { folder: home.folder ?? '' });
        else await api.updateFolder(item.id, { parent: home.folder ?? '' });
      }
    };
    const homeOf = (id: string) => docs?.find(d => d.id === id)?.parent;
    return {
      docs,
      trash,
      folders,
      error,
      canWrite,
      reload,
      patchDoc: patchLocal,
      trail,
      setTrail,
      accepts: (item, target) => {
        if (!item || !canWrite || item.shortcut) return false;
        if (item.kind === 'folder') return canMoveFolder(folders, item.id, target);
        const d = docs?.find(x => x.id === item.id);
        return !!d && (d.parent ? true : d.folder !== target);
      },
      move: (item, target) =>
        act(async () => {
          if (item.kind === 'doc') {
            const old = homeOf(item.id);
            patchLocal(item.id, { folder: target, parent: undefined });
            await api.updateBoard(item.id, { folder: target });
            if (old) await unlinkQuietly(old, item.id);
          } else {
            setFolders(fs => fs.map(f => (f.id === item.id ? { ...f, parent: target } : f)));
            await api.updateFolder(item.id, { parent: target });
          }
        }),
      place: (item, parent, index) =>
        act(async () => {
          const old = item.kind === 'doc' ? homeOf(item.id) : undefined;
          // The order is among what the level shows: documents living in
          // another document are not at the folder level.
          await writeRanks(placeAt(folders, atFolderLevel(docs ?? []), parent, item, index), item, { folder: parent });
          if (old) await unlinkQuietly(old, item.id);
        }),
      placeInDoc: (item, parent, index) =>
        act(async () => {
          const p = docs?.find(d => d.id === parent);
          if (!p) return;
          const old = homeOf(item.id);
          const siblings = inside(docs ?? [], p).docs.map(d => ({ kind: 'doc' as const, id: d.id, rank: d.rank }));
          await writeRanks(placeAmong(siblings, item, index), item, { parent });
          // Its parent's body links to it, as "+" does; where it came from
          // no longer does (unless the text there mentions it).
          await api.addChild(parent, item.id);
          if (old && old !== parent) await unlinkQuietly(old, item.id);
        }),
      placeShortcut: (item, parent, at) =>
        act(async () => {
          // Out of the old place first: when that is refused (it is
          // mentioned in the text there), nothing has moved.
          if (item.from && item.from !== parent) await api.removeChild(item.from, item.id);
          await api.addChild(parent, item.id, at);
        }),
      createDoc: (mode, folder, title = '') => act(() => api.createBoard({ mode, folder, title })),
      createChild: (mode, parent) =>
        act(async () => {
          const b = await api.createBoard({ mode, parent });
          await api.addChild(parent, b.id);
          return b;
        }),
      makeOriginal: (doc, parent) =>
        act(async () => {
          const old = homeOf(doc);
          patchLocal(doc, { parent, folder: '' });
          await api.updateBoard(doc, { parent });
          // Where it lived keeps a shortcut to it.
          if (old) await api.addChild(old, doc);
        }),
      copyShortcut: (doc, parent) => act(() => api.copyBoard(doc, parent)),
      removeShortcut: (doc, parent) => act(() => api.removeChild(parent, doc)),
      createFolder: (name, parent) => act(() => api.createFolder(name, parent)),
      renameFolder: (id, name) => act(async () => void (await api.updateFolder(id, { name }))),
      deleteFolder: id => act(() => api.deleteFolder(id)),
      trashDoc: id => setTrashed(id, true),
      restoreDoc: id => setTrashed(id, false),
      deleteDoc: id =>
        act(async () => {
          setDocs(ds => ds?.filter(d => d.id !== id) ?? ds);
          await api.deleteBoard(id);
        }),
    };
  }, [docs, trash, folders, error, canWrite, reload, trail]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

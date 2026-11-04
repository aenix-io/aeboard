// The document catalog — documents and folders — shared by the start page
// and the sidebar, so a change made in one shows in the other at once.
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { api, type Board, type Folder } from './api';
import { canMoveFolder, placeAt, topLevel } from './library';

export type Mode = 'edgeless' | 'page';

/** What is being dragged: a document or a folder. */
export type Dragged = { kind: 'doc' | 'folder'; id: string };

export const DRAG_TYPE = 'application/x-aenotes-item';

const EXPECT_CHILD = 'aenotes:expect-child';
/** How long a link announced by expectChild is shown before the server
 *  must have it. */
const EXPECT_MS = 15_000;

/** expectChild announces that child is about to be linked from parent (a
 *  document the editor creates from inside another): the catalog shows it
 *  there until the server's own list has the link. */
export function expectChild(parent: string, child: string): void {
  window.dispatchEvent(new CustomEvent(EXPECT_CHILD, { detail: { parent, child } }));
}

export function dragData(e: React.DragEvent): Dragged | null {
  try {
    const v = JSON.parse(e.dataTransfer.getData(DRAG_TYPE)) as Dragged;
    return v && (v.kind === 'doc' || v.kind === 'folder') && typeof v.id === 'string' ? v : null;
  } catch {
    return null;
  }
}

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
  /** accepts reports whether item may be dropped into target ("" = top). */
  accepts(item: Dragged | null, target: string): boolean;
  move(item: Dragged, target: string): Promise<void>;
  /** place puts item at `index` of folder `parent`'s entries (counted
   *  without the item): the order people set by dragging. */
  place(item: Dragged, parent: string, index: number): Promise<void>;
  createDoc(mode: Mode, folder: string, title?: string): Promise<Board>;
  /** createChild makes a new document inside parent (linked from it). */
  createChild(mode: Mode, parent: string): Promise<Board>;
  /** acceptsChild reports whether doc may be put inside parent. */
  acceptsChild(item: Dragged | null, parent: string): boolean;
  /** addChild puts an existing document inside parent. */
  addChild(parent: string, doc: string): Promise<void>;
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

  // Links announced by expectChild, by child, with when they expire.
  const [expected, setExpected] = useState<Map<string, { parent: string; until: number }>>(new Map());
  useEffect(() => {
    const on = (e: Event) => {
      const { parent, child } = (e as CustomEvent<{ parent: string; child: string }>).detail;
      setExpected(m => new Map(m).set(child, { parent, until: Date.now() + EXPECT_MS }));
      setTimeout(() => setExpected(m => (m.has(child) ? new Map([...m].filter(([c]) => c !== child)) : m)), EXPECT_MS);
    };
    window.addEventListener(EXPECT_CHILD, on);
    return () => window.removeEventListener(EXPECT_CHILD, on);
  }, []);

  const docs = useMemo(() => {
    const live = all?.filter(d => !d.trashed) ?? null;
    if (!live || expected.size === 0) return live;
    const now = Date.now();
    return live.map(d => {
      const add = [...expected]
        .filter(([c, x]) => x.parent === d.id && x.until > now && !(d.links ?? []).includes(c))
        .map(([c]) => c);
      return add.length ? { ...d, links: [...(d.links ?? []), ...add] } : d;
    });
  }, [all, expected]);
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
    const setTrashed = (id: string, on: boolean) =>
      act(async () => {
        // Show it at once; the reload confirms it.
        const at = on ? new Date().toISOString() : undefined;
        setDocs(ds => ds?.map(d => (d.id === id ? { ...d, trashed: at } : d)) ?? ds);
        await api.updateBoard(id, { trashed: on });
      });
    return {
      docs,
      trash,
      folders,
      error,
      canWrite,
      reload,
      patchDoc: (id, patch) => setDocs(ds => ds?.map(d => (d.id === id ? { ...d, ...patch } : d)) ?? ds),
      accepts: (item, target) => {
        if (!item || !canWrite) return false;
        if (item.kind === 'folder') return canMoveFolder(folders, item.id, target);
        return (docs?.find(d => d.id === item.id)?.folder ?? '') !== target;
      },
      move: (item, target) =>
        act(async () => {
          // Show the move at once; the reload confirms it.
          if (item.kind === 'doc') {
            setDocs(ds => ds?.map(d => (d.id === item.id ? { ...d, folder: target } : d)) ?? ds);
            await api.updateBoard(item.id, { folder: target });
          } else {
            setFolders(fs => fs.map(f => (f.id === item.id ? { ...f, parent: target } : f)));
            await api.updateFolder(item.id, { parent: target });
          }
        }),
      place: (item, parent, index) =>
        act(async () => {
          // The order is among what the level shows: documents nested in
          // another one are not at the top.
          const ps = placeAt(folders, topLevel(folders, docs ?? []), parent, item, index);
          const rankOf = (kind: string, id: string) => ps.find(p => p.kind === kind && p.id === id)?.rank;
          // Show the new order at once; the reload confirms it.
          setDocs(
            ds =>
              ds?.map(d => {
                const r = rankOf('doc', d.id);
                if (item.kind === 'doc' && d.id === item.id) return { ...d, folder: parent, rank: r ?? d.rank };
                return r ? { ...d, rank: r } : d;
              }) ?? ds,
          );
          setFolders(fs =>
            fs.map(f => {
              const r = rankOf('folder', f.id);
              if (item.kind === 'folder' && f.id === item.id) return { ...f, parent, rank: r ?? f.rank };
              return r ? { ...f, rank: r } : f;
            }),
          );
          for (const p of ps) {
            const moving = p.kind === item.kind && p.id === item.id;
            if (p.kind === 'doc') await api.updateBoard(p.id, moving ? { folder: parent, rank: p.rank } : { rank: p.rank });
            else await api.updateFolder(p.id, moving ? { parent, rank: p.rank } : { rank: p.rank });
          }
        }),
      createDoc: (mode, folder, title = '') => act(() => api.createBoard({ mode, folder, title })),
      createChild: (mode, parent) =>
        act(async () => {
          const b = await api.createBoard({ mode });
          await api.addChild(parent, b.id);
          return b;
        }),
      acceptsChild: (item, parent) => {
        if (!item || !canWrite || item.kind !== 'doc' || item.id === parent) return false;
        return !(docs?.find(d => d.id === parent)?.links ?? []).includes(item.id);
      },
      addChild: (parent, doc) =>
        act(async () => {
          // Show it inside at once; the reload confirms it.
          setDocs(ds => ds?.map(d => (d.id === parent ? { ...d, links: [...(d.links ?? []), doc] } : d)) ?? ds);
          await api.addChild(parent, doc);
        }),
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
  }, [docs, trash, folders, error, canWrite, reload]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

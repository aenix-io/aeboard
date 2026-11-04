// The document catalog — documents and folders — shared by the start page
// and the sidebar, so a change made in one shows in the other at once.
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { api, type Board, type Folder } from './api';
import { canMoveFolder, placeAt } from './library';

export type Mode = 'edgeless' | 'page';

/** What is being dragged: a document or a folder. */
export type Dragged = { kind: 'doc' | 'folder'; id: string };

export const DRAG_TYPE = 'application/x-aenotes-item';

export function dragData(e: React.DragEvent): Dragged | null {
  try {
    const v = JSON.parse(e.dataTransfer.getData(DRAG_TYPE)) as Dragged;
    return v && (v.kind === 'doc' || v.kind === 'folder') && typeof v.id === 'string' ? v : null;
  } catch {
    return null;
  }
}

export interface Catalog {
  docs: Board[] | null;
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
  createFolder(name: string, parent: string): Promise<Folder>;
  renameFolder(id: string, name: string): Promise<void>;
  deleteFolder(id: string): Promise<void>;
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
  const [docs, setDocs] = useState<Board[] | null>(null);
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
    return {
      docs,
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
          const ps = placeAt(folders, docs ?? [], parent, item, index);
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
      createFolder: (name, parent) => act(() => api.createFolder(name, parent)),
      renameFolder: (id, name) => act(async () => void (await api.updateFolder(id, { name }))),
      deleteFolder: id => act(() => api.deleteFolder(id)),
    };
  }, [docs, folders, error, canWrite, reload]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

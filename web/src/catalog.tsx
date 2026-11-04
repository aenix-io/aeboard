// The document catalog — documents and folders — shared by the start page
// and the sidebar, so a change made in one shows in the other at once.
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { api, type Board, type Folder } from './api';
import { canMoveFolder } from './library';

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

/** How often the catalog is refreshed in the background (others' changes). */
const REFRESH_MS = 30_000;

export function CatalogProvider({ canWrite, children }: { canWrite: boolean; children: ReactNode }) {
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
      createDoc: (mode, folder, title = '') => act(() => api.createBoard({ mode, folder, title })),
      createFolder: (name, parent) => act(() => api.createFolder(name, parent)),
      renameFolder: (id, name) => act(async () => void (await api.updateFolder(id, { name }))),
      deleteFolder: id => act(() => api.deleteFolder(id)),
    };
  }, [docs, folders, error, canWrite, reload]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

import { type DragEvent, type FormEvent, type ReactNode, useMemo, useState } from 'react';

import type { Board, Folder } from '../api';
import { DRAG_TYPE, type Dragged, dragData, type Mode, useCatalog } from '../catalog';
import { countIn, type Entry, entriesOf, filterKind, inFolder, type KindFilter, pathOf, search } from '../library';
import { folderPath, navigate } from '../router';
import { untitled } from './BoardPage';
import { BoardIcon, FolderIcon, PageIcon } from './icons';

const when = (iso: string) => {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
};


interface Props {
  /** The folder on screen; "" is the top level. */
  folder: string;
  /** The sidebar toggle, first in the header. */
  lead?: ReactNode;
  whoami: ReactNode;
}

/** BoardList is the start page: a folder of pages and boards, browsed like
 *  files — breadcrumbs, subfolders, drag and drop between folders. */
export function BoardList({ folder, lead, whoami }: Props) {
  const cat = useCatalog();
  const { docs, folders, canWrite } = cat;
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [kind, setKind] = useState<KindFilter>(loadKind);
  // The kind of document being created, or null while the form is closed.
  const [creating, setCreating] = useState<Mode | null>(null);
  const [newFolder, setNewFolder] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [moving, setMoving] = useState<Dragged | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);

  const act = async (f: () => Promise<unknown>) => {
    try {
      setError(null);
      await f();
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  };
  const moveInto = (item: Dragged, target: string) => act(() => cat.move(item, target));
  const accepts = cat.accepts;

  // Drag and drop: cards are dragged, folder tiles and breadcrumbs take them.
  const dragProps = (item: Dragged) =>
    canWrite
      ? {
          draggable: true,
          onDragStart: (e: DragEvent) => {
            e.dataTransfer.setData(DRAG_TYPE, JSON.stringify(item));
            e.dataTransfer.effectAllowed = 'move';
          },
        }
      : {};
  const dropProps = (target: string) => ({
    onDragOver: (e: DragEvent) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      setDropTarget(target);
    },
    onDragLeave: () => setDropTarget(t => (t === target ? null : t)),
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      setDropTarget(null);
      const item = dragData(e);
      if (item && accepts(item, target)) void moveInto(item, target);
    },
    'data-drop': dropTarget === target ? 'over' : undefined,
  });

  const path = pathOf(folders, folder);
  const here = useMemo(() => inFolder(folders, docs ?? [], folder), [folders, docs, folder]);
  const searching = query.trim() !== '';
  const shownDocs = filterKind(searching ? search(docs ?? [], query) : here.docs, kind);
  const shownFolders = searching ? [] : here.folders;
  const missing = folder !== '' && docs !== null && path.length === 0;
  const empty = docs !== null && !searching && shownFolders.length === 0 && here.docs.length === 0;

  const open = (href: string) => (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    navigate(href);
  };

  // Cards in the folder's order — the order set by dragging in the sidebar —
  // folders and documents together, as the sidebar shows them.
  const folderCard = (f: Folder) =>
    renaming === f.id ? (
            <li key={f.id}>
              <NameForm
                initial={f.name}
                placeholder="Folder name"
                submit="Rename"
                onCancel={() => setRenaming(null)}
                onSubmit={name =>
                  act(async () => {
                    await cat.renameFolder(f.id, name);
                    setRenaming(null);
                  })
                }
              />
            </li>
          ) : (
            <li key={f.id}>
              <a
                className="board-card folder-card"
                href={folderPath(f.id)}
                onClick={open(folderPath(f.id))}
                {...dragProps({ kind: 'folder', id: f.id })}
                {...dropProps(f.id)}
              >
                <span className="board-title">
                  <span className="kind kind-folder" aria-label="Folder">
                    <FolderIcon />
                  </span>
                  {f.name}
                </span>
                <span className="board-meta">{plural(countIn(folders, docs ?? [], f.id), 'item')}</span>
                {canWrite && (
                  <ItemMenu
                    items={[
                      ['Rename', () => setRenaming(f.id)],
                      ['Move to…', () => setMoving({ kind: 'folder', id: f.id })],
                      ['Delete', () => act(() => cat.deleteFolder(f.id))],
                    ]}
                  />
                )}
              </a>
            </li>
          );
  const docCard = (b: Board) => (

          <li key={b.id}>
            <a className="board-card" href={`/b/${b.id}`} onClick={open(`/b/${b.id}`)} {...dragProps({ kind: 'doc', id: b.id })}>
              <span className="board-title">
                <span className={`kind kind-${b.mode}`} aria-label={b.mode === 'page' ? 'Page' : 'Board'}>
                  {b.mode === 'page' ? <PageIcon /> : <BoardIcon />}
                </span>
                {b.title || untitled(b.mode)}
              </span>
              <span className="board-meta">
                {searching && pathOf(folders, b.folder).length > 0 && (
                  <>{pathOf(folders, b.folder).map(f => f.name).join(' / ')} · </>
                )}
                Updated {when(b.updated)}
              </span>
              {canWrite && <ItemMenu items={[['Move to…', () => setMoving({ kind: 'doc', id: b.id })]]} />}
            </a>
          </li>
  );
  const shown: Entry<Board>[] = searching
    ? shownDocs.map(d => ({ kind: 'doc' as const, id: d.id, rank: d.rank, name: d.title, doc: d }))
    : entriesOf(folders, docs ?? [], folder).filter(e => e.kind === 'folder' || filterKind([e.doc], kind).length > 0);

  return (
    <div className="page">
      <header className="list-head">
        {lead}
        <nav className="crumbs" aria-label="Folder">
          <a href="/" className="crumb" onClick={open('/')} {...dropProps('')}>
            All
          </a>
          {path.map(f => (
            <span key={f.id} className="crumb-sep-wrap">
              <span className="crumb-sep">/</span>
              <a href={folderPath(f.id)} className="crumb" onClick={open(folderPath(f.id))} {...dropProps(f.id)}>
                {f.name}
              </a>
            </span>
          ))}
        </nav>
        <div className="list-tools">
          <input
            className="search"
            type="search"
            placeholder="Search everywhere…"
            value={query}
            onChange={e => setQuery(e.target.value)}
          />
          {canWrite && !missing && (
            <>
              <button type="button" className="primary" onClick={() => setCreating('page')}>
                New page
              </button>
              <button type="button" onClick={() => setCreating('edgeless')}>
                New board
              </button>
              <button type="button" onClick={() => setNewFolder(true)}>
                New folder
              </button>
            </>
          )}
          {whoami}
        </div>
      </header>

      <div className="list-filters">
        <div className="segmented" role="tablist" aria-label="Kind">
          {KINDS.map(([k, label]) => (
            <button
              key={k}
              type="button"
              role="tab"
              aria-selected={kind === k}
              className={`segment${kind === k ? ' segment-active' : ''}`}
              onClick={() => {
                setKind(k);
                saveKind(k);
              }}
            >
              {label}
            </button>
          ))}
        </div>
        {canWrite && <span className="muted hint">Drag pages, boards and folders onto a folder to move them.</span>}
      </div>

      {creating && <CreateDoc mode={creating} folder={folder} onCancel={() => setCreating(null)} />}
      {newFolder && (
        <NameForm
          placeholder="Folder name"
          submit="Create folder"
          onCancel={() => setNewFolder(false)}
          onSubmit={name =>
            act(async () => {
              await cat.createFolder(name, folder);
              setNewFolder(false);
            })
          }
        />
      )}
      {(error ?? cat.error) && <p className="error">{error ?? cat.error}</p>}
      {docs === null && !cat.error && <p className="muted">Loading…</p>}
      {missing && <p className="muted">This folder is not there any more.</p>}
      {empty && !missing && !creating && (
        <div className="empty">
          <p>{folder ? 'This folder is empty.' : 'Nothing here yet.'}</p>
          {canWrite && (
            <>
              <button type="button" className="primary" onClick={() => setCreating('page')}>
                Create a page
              </button>{' '}
              <button type="button" onClick={() => setCreating('edgeless')}>
                or a board
              </button>
            </>
          )}
        </div>
      )}
      {searching && shownDocs.length === 0 && <p className="muted">Nothing matches.</p>}

      <ul className="board-grid">
        {shown.map(e => (e.kind === 'folder' ? folderCard(e.folder) : docCard(e.doc)))}
      </ul>

      {moving && (
        <MoveDialog
          folders={folders}
          item={moving}
          accepts={target => accepts(moving, target)}
          onCancel={() => setMoving(null)}
          onMove={target => {
            const item = moving;
            setMoving(null);
            void moveInto(item, target);
          }}
        />
      )}
    </div>
  );
}

/** ItemMenu is a card's "⋯" menu; clicks inside it never open the card. */
function ItemMenu({ items }: { items: Array<[string, () => void]> }) {
  const [open, setOpen] = useState(false);
  const stop = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
  };
  return (
    <span className="item-menu" onClick={stop}>
      <button
        type="button"
        className="item-menu-button"
        aria-label="More"
        aria-expanded={open}
        onClick={e => {
          stop(e);
          setOpen(o => !o);
        }}
      >
        ⋯
      </button>
      {open && (
        <span className="item-menu-list" role="menu" onMouseLeave={() => setOpen(false)}>
          {items.map(([label, run]) => (
            <button
              key={label}
              type="button"
              role="menuitem"
              onClick={e => {
                stop(e);
                setOpen(false);
                run();
              }}
            >
              {label}
            </button>
          ))}
        </span>
      )}
    </span>
  );
}

/** MoveDialog picks a folder to move a document or a folder into. */
function MoveDialog({
  folders,
  item,
  accepts,
  onMove,
  onCancel,
}: {
  folders: Folder[];
  item: Dragged;
  accepts: (target: string) => boolean;
  onMove: (target: string) => void;
  onCancel: () => void;
}) {
  // The tree, depth-first, each folder under its parent.
  const rows: Array<{ id: string; name: string; depth: number }> = [{ id: '', name: 'All (top level)', depth: 0 }];
  const walk = (parent: string, depth: number) => {
    for (const f of inFolder(folders, [], parent).folders) {
      if (item.kind === 'folder' && f.id === item.id) continue; // not into itself or below
      rows.push({ id: f.id, name: f.name, depth });
      walk(f.id, depth + 1);
    }
  };
  walk('', 1);
  return (
    <div className="dialog-scrim" onClick={onCancel}>
      <div className="dialog" role="dialog" aria-label="Move to" onClick={e => e.stopPropagation()}>
        <h2>Move to</h2>
        <ul className="move-list">
          {rows.map(r => (
            <li key={r.id || 'top'}>
              <button
                type="button"
                disabled={!accepts(r.id)}
                style={{ paddingLeft: `${8 + r.depth * 16}px` }}
                onClick={() => onMove(r.id)}
              >
                {r.id ? <FolderIcon /> : null} {r.name}
              </button>
            </li>
          ))}
        </ul>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  );
}

function NameForm({
  initial = '',
  placeholder,
  submit,
  onSubmit,
  onCancel,
}: {
  initial?: string;
  placeholder: string;
  submit: string;
  onSubmit: (name: string) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial);
  return (
    <form
      className="create"
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        if (name.trim()) onSubmit(name.trim());
      }}
    >
      <input
        autoFocus
        placeholder={placeholder}
        value={name}
        onChange={e => setName(e.target.value)}
        onKeyDown={e => e.key === 'Escape' && onCancel()}
      />
      <button type="submit" className="primary" disabled={!name.trim()}>
        {submit}
      </button>
      <button type="button" onClick={onCancel}>
        Cancel
      </button>
    </form>
  );
}

function CreateDoc({ mode, folder, onCancel }: { mode: Mode; folder: string; onCancel: () => void }) {
  const cat = useCatalog();
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      // New documents go into the folder on screen.
      const b = await cat.createDoc(mode, folder, title);
      navigate(`/b/${b.id}`);
    } catch (err) {
      setError(String((err as Error).message ?? err));
      setBusy(false);
    }
  };
  return (
    <form className="create" onSubmit={submit}>
      <input
        autoFocus
        placeholder={mode === 'page' ? 'Page title' : 'Board title'}
        value={title}
        onChange={e => setTitle(e.target.value)}
        onKeyDown={e => e.key === 'Escape' && onCancel()}
      />
      <button type="submit" className="primary" disabled={busy}>
        Create
      </button>
      <button type="button" onClick={onCancel}>
        Cancel
      </button>
      {error && <span className="error">{error}</span>}
    </form>
  );
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

const KINDS: Array<[KindFilter, string]> = [
  ['all', 'All'],
  ['page', 'Pages'],
  ['edgeless', 'Boards'],
];

const KIND_KEY = 'aenotes.kindFilter';

function loadKind(): KindFilter {
  try {
    const v = localStorage.getItem(KIND_KEY);
    return v === 'edgeless' || v === 'page' ? v : 'all';
  } catch {
    return 'all';
  }
}

function saveKind(k: KindFilter) {
  try {
    localStorage.setItem(KIND_KEY, k);
  } catch {
    /* storage blocked */
  }
}




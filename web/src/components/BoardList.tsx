import { type DragEvent, type FormEvent, type ReactNode, useMemo, useState } from 'react';

import type { Board, Folder } from '../api';
import { DRAG_TYPE, type Dragged, type Mode, useCatalog } from '../catalog';
import { filterKind, inFolder, type KindFilter, pathOf, search } from '../library';
import { navigate } from '../router';
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
  /** The sidebar toggle, first in the header. */
  lead?: ReactNode;
  whoami: ReactNode;
  /** The sync badge, left of the other tools. */
  status?: ReactNode;
}

/** BoardList is the start page: every page and board, most recently
 *  updated first, as AFFiNE's "All docs" — the structure (folders and
 *  documents inside documents) lives in the sidebar. */
export function BoardList({ lead, whoami, status }: Props) {
  const cat = useCatalog();
  const { docs, folders, canWrite } = cat;
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [kind, setKind] = useState<KindFilter>(loadKind);
  // The kind of document being created, or null while the form is closed.
  const [creating, setCreating] = useState<Mode | null>(null);
  const [moving, setMoving] = useState<Dragged | null>(null);

  const act = async (f: () => Promise<unknown>) => {
    try {
      setError(null);
      await f();
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  };

  // Cards drag into the sidebar: onto a folder, or onto a document to put
  // them inside it.
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

  const searching = query.trim() !== '';
  const shown = useMemo(() => {
    const all = docs ?? [];
    return filterKind(searching ? search(all, query) : all, kind);
  }, [docs, query, searching, kind]);

  const open = (href: string) => (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    navigate(href);
  };

  const docCard = (b: Board) => {
    const where = pathOf(folders, b.folder)
      .map(f => f.name)
      .join(' / ');
    return (
      <li key={b.id}>
        <a className="board-card" href={`/b/${b.id}`} onClick={open(`/b/${b.id}`)} {...dragProps({ kind: 'doc', id: b.id })}>
          <span className="board-title">
            <span className={`kind kind-${b.mode}`} aria-label={b.mode === 'page' ? 'Page' : 'Board'}>
              {b.mode === 'page' ? <PageIcon /> : <BoardIcon />}
            </span>
            {b.title || untitled(b.mode)}
          </span>
          <span className="board-meta">
            {where && <>{where} · </>}
            Updated {when(b.updated)}
          </span>
          {canWrite && <ItemMenu items={[['Move to folder…', () => setMoving({ kind: 'doc', id: b.id })]]} />}
        </a>
      </li>
    );
  };

  return (
    <div className="page">
      <header className="list-head">
        {lead}
        <h1 className="list-title">All documents</h1>
        <div className="list-tools">
          {status}
          <input
            className="search"
            type="search"
            placeholder="Search…"
            value={query}
            onChange={e => setQuery(e.target.value)}
          />
          {canWrite && (
            <>
              <button type="button" className="primary" onClick={() => setCreating('page')}>
                New page
              </button>
              <button type="button" onClick={() => setCreating('edgeless')}>
                New board
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
      </div>

      {creating && <CreateDoc mode={creating} onCancel={() => setCreating(null)} />}
      {(error ?? cat.error) && <p className="error">{error ?? cat.error}</p>}
      {docs === null && !cat.error && <p className="muted">Loading…</p>}
      {docs !== null && docs.length === 0 && !creating && (
        <div className="empty">
          <p>Nothing here yet.</p>
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
      {docs !== null && docs.length > 0 && shown.length === 0 && <p className="muted">Nothing matches.</p>}

      <ul className="board-grid">{shown.map(docCard)}</ul>

      {moving && (
        <MoveDialog
          folders={folders}
          item={moving}
          accepts={target => cat.accepts(moving, target)}
          onCancel={() => setMoving(null)}
          onMove={target => {
            const item = moving;
            setMoving(null);
            void act(() => cat.move(item, target));
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

function CreateDoc({ mode, onCancel }: { mode: Mode; onCancel: () => void }) {
  const cat = useCatalog();
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const b = await cat.createDoc(mode, '', title);
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




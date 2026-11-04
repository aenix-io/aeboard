import { type DragEvent, type ReactNode, useEffect, useMemo, useRef, useState } from 'react';

import type { Board, Folder } from '../api';
import { DRAG_TYPE, type Dragged, dragData, type Mode, useCatalog } from '../catalog';
import { inFolder, pathOf } from '../library';
import { folderPath, navigate, type Route } from '../router';
import { untitled } from './BoardPage';
import { BoardIcon, FolderIcon, PageIcon } from './icons';

const OPEN_KEY = 'aenotes.sidebar.expanded';

function loadExpanded(): Set<string> {
  try {
    const v = JSON.parse(localStorage.getItem(OPEN_KEY) ?? '[]');
    return new Set(Array.isArray(v) ? (v as string[]) : []);
  } catch {
    return new Set();
  }
}

function saveExpanded(s: Set<string>) {
  try {
    localStorage.setItem(OPEN_KEY, JSON.stringify([...s]));
  } catch {
    /* storage blocked */
  }
}

/** Sidebar is the Notion-style tree of folders, pages and boards: expand
 *  folders in place, create anything anywhere from "+", drag to move. */
export function Sidebar({ route }: { route: Route }) {
  const cat = useCatalog();
  const [expanded, setExpanded] = useState<Set<string>>(loadExpanded);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  // The folder an inline "new folder" field is open in, or null.
  const [newFolderIn, setNewFolderIn] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);

  const docs = useMemo(() => cat.docs ?? [], [cat.docs]);
  const currentDoc = route.name === 'board' ? route.id : '';
  const currentFolder = route.name === 'list' ? route.folder : (docs.find(d => d.id === currentDoc)?.folder ?? '');

  // Open the way down to where the visitor is.
  useEffect(() => {
    const up = pathOf(cat.folders, currentFolder).map(f => f.id);
    if (up.every(id => expanded.has(id))) return;
    const next = new Set(expanded);
    up.forEach(id => next.add(id));
    setExpanded(next);
    saveExpanded(next);
  }, [currentFolder, cat.folders]); // eslint-disable-line react-hooks/exhaustive-deps

  const toggle = (id: string, open?: boolean) => {
    const next = new Set(expanded);
    if (open ?? !next.has(id)) next.add(id);
    else next.delete(id);
    setExpanded(next);
    saveExpanded(next);
  };

  const createDoc = async (mode: Mode, folder: string) => {
    if (folder) toggle(folder, true);
    try {
      const b = await cat.createDoc(mode, folder);
      navigate(`/b/${b.id}`);
    } catch {
      /* shown by the catalog's error */
    }
  };

  const drag = (item: Dragged) =>
    cat.canWrite
      ? {
          draggable: true,
          onDragStart: (e: DragEvent) => {
            e.stopPropagation();
            e.dataTransfer.setData(DRAG_TYPE, JSON.stringify(item));
            e.dataTransfer.effectAllowed = 'move';
          },
        }
      : {};
  const drop = (target: string) => ({
    onDragOver: (e: DragEvent) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      e.dataTransfer.dropEffect = 'move';
      setDropTarget(target);
    },
    onDragLeave: () => setDropTarget(t => (t === target ? null : t)),
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      setDropTarget(null);
      const item = dragData(e);
      if (item && cat.accepts(item, target)) {
        if (target) toggle(target, true);
        void cat.move(item, target).catch(() => {});
      }
    },
    'data-drop': dropTarget === target ? 'over' : undefined,
  });

  const go = (href: string) => (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    navigate(href);
  };

  const renderLevel = (parent: string, depth: number): ReactNode => {
    const here = inFolder(cat.folders, docs, parent);
    const sortedDocs = [...here.docs].sort((a, b) => (a.title || '~').localeCompare(b.title || '~'));
    return (
      <>
        {newFolderIn === parent && (
          <NameRow
            depth={depth}
            placeholder="Folder name"
            onCancel={() => setNewFolderIn(null)}
            onSubmit={name => {
              setNewFolderIn(null);
              void cat.createFolder(name, parent).catch(() => {});
            }}
          />
        )}
        {here.folders.map(f => folderRow(f, depth))}
        {sortedDocs.map(d => docRow(d, depth))}
        {here.folders.length === 0 && sortedDocs.length === 0 && newFolderIn !== parent && depth > 0 && (
          <div className="sb-empty" style={{ paddingLeft: indent(depth) + 22 }}>
            Empty
          </div>
        )}
      </>
    );
  };

  const folderRow = (f: Folder, depth: number) => {
    const open = expanded.has(f.id);
    if (renaming === f.id) {
      return (
        <NameRow
          key={f.id}
          depth={depth}
          initial={f.name}
          placeholder="Folder name"
          onCancel={() => setRenaming(null)}
          onSubmit={name => {
            setRenaming(null);
            void cat.renameFolder(f.id, name).catch(() => {});
          }}
        />
      );
    }
    return (
      <div key={f.id} className="sb-node">
        <div
          className={`sb-row${currentFolder === f.id && route.name === 'list' ? ' sb-active' : ''}`}
          style={{ paddingLeft: indent(depth) }}
          {...drag({ kind: 'folder', id: f.id })}
          {...drop(f.id)}
        >
          <button
            type="button"
            className="sb-caret"
            aria-label={open ? `Collapse ${f.name}` : `Expand ${f.name}`}
            aria-expanded={open}
            onClick={() => toggle(f.id)}
          >
            {open ? '▾' : '▸'}
          </button>
          <a className="sb-label" href={folderPath(f.id)} onClick={go(folderPath(f.id))}>
            <span className="kind kind-folder">
              <FolderIcon />
            </span>
            <span className="sb-name">{f.name}</span>
          </a>
          {cat.canWrite && (
            <span className="sb-actions">
              <RowMenu
                label="Add to folder"
                glyph="+"
                items={[
                  ['New page', () => void createDoc('page', f.id)],
                  ['New board', () => void createDoc('edgeless', f.id)],
                  [
                    'New folder',
                    () => {
                      toggle(f.id, true);
                      setNewFolderIn(f.id);
                    },
                  ],
                ]}
              />
              <RowMenu
                label="Folder actions"
                glyph="⋯"
                items={[
                  ['Rename', () => setRenaming(f.id)],
                  ['Delete', () => void cat.deleteFolder(f.id).catch(() => {})],
                ]}
              />
            </span>
          )}
        </div>
        {open && renderLevel(f.id, depth + 1)}
      </div>
    );
  };

  const docRow = (d: Board, depth: number) => (
    <div
      key={d.id}
      className={`sb-row${d.id === currentDoc ? ' sb-active' : ''}`}
      style={{ paddingLeft: indent(depth) }}
      {...drag({ kind: 'doc', id: d.id })}
    >
      <span className="sb-caret sb-caret-none" />
      <a className="sb-label" href={`/b/${d.id}`} onClick={go(`/b/${d.id}`)}>
        <span className={`kind kind-${d.mode}`}>{d.mode === 'page' ? <PageIcon /> : <BoardIcon />}</span>
        <span className={`sb-name${d.title ? '' : ' sb-untitled'}`}>{d.title || untitled(d.mode)}</span>
      </a>
    </div>
  );

  return (
    <aside className="sidebar" aria-label="Pages and boards">
      <div className="sb-head">
        <a className="sb-home" href="/" onClick={go('/')} {...drop('')}>
          aenotes
        </a>
        {cat.canWrite && (
          <RowMenu
            label="Add"
            glyph="+"
            items={[
              ['New page', () => void createDoc('page', '')],
              ['New board', () => void createDoc('edgeless', '')],
              ['New folder', () => setNewFolderIn('')],
            ]}
          />
        )}
      </div>
      <div className="sb-tree">
        {cat.docs === null ? <div className="sb-empty">Loading…</div> : renderLevel('', 0)}
        {/* The space below the tree takes drops to the top level. */}
        <div className="sb-rest" {...drop('')} />
      </div>
      {cat.error && <div className="sb-error error">{cat.error}</div>}
    </aside>
  );
}

const indent = (depth: number) => 8 + depth * 14;

/** RowMenu is a row's small hover button with a dropdown. The list is fixed
 *  to the window (the tree scrolls and would clip it) and closes on a click
 *  elsewhere, Escape or a choice — not when the pointer wanders off, which
 *  would close it on the way from the button to the list. */
function RowMenu({ label, glyph, items }: { label: string; glyph: string; items: Array<[string, () => void]> }) {
  const [at, setAt] = useState<{ top: number; left: number } | null>(null);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent && e.key !== 'Escape') return;
      if (e instanceof PointerEvent && root.current?.contains(e.target as Node)) return;
      setAt(null);
    };
    document.addEventListener('pointerdown', close, true);
    document.addEventListener('keydown', close);
    window.addEventListener('resize', close);
    return () => {
      document.removeEventListener('pointerdown', close, true);
      document.removeEventListener('keydown', close);
      window.removeEventListener('resize', close);
    };
  }, [at]);
  return (
    <span className="sb-menu" ref={root}>
      <button
        type="button"
        className="sb-icon-button"
        aria-label={label}
        title={label}
        aria-expanded={at !== null}
        onClick={e => {
          e.preventDefault();
          e.stopPropagation();
          if (at) {
            setAt(null);
            return;
          }
          const r = e.currentTarget.getBoundingClientRect();
          setAt({ top: r.bottom + 4, left: Math.min(r.left, window.innerWidth - 180) });
        }}
      >
        {glyph}
      </button>
      {at && (
        <span className="item-menu-list sb-menu-list" role="menu" style={{ position: 'fixed', top: at.top, left: at.left }}>
          {items.map(([text, run]) => (
            <button
              key={text}
              type="button"
              role="menuitem"
              onClick={e => {
                e.stopPropagation();
                setAt(null);
                run();
              }}
            >
              {text}
            </button>
          ))}
        </span>
      )}
    </span>
  );
}

function NameRow({
  depth,
  initial = '',
  placeholder,
  onSubmit,
  onCancel,
}: {
  depth: number;
  initial?: string;
  placeholder: string;
  onSubmit: (name: string) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial);
  const done = () => (name.trim() ? onSubmit(name.trim()) : onCancel());
  return (
    <div className="sb-row" style={{ paddingLeft: indent(depth) + 22 }}>
      <input
        className="sb-input"
        autoFocus
        value={name}
        placeholder={placeholder}
        onChange={e => setName(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Enter') done();
          if (e.key === 'Escape') onCancel();
        }}
        onBlur={done}
      />
    </div>
  );
}

/** SidebarToggle is the header button that shows and hides the sidebar. */
export function SidebarToggle({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      className="sidebar-toggle"
      aria-label={open ? 'Hide sidebar' : 'Show sidebar'}
      title={open ? 'Hide sidebar' : 'Show sidebar'}
      aria-pressed={open}
      onClick={onToggle}
    >
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
        <rect x="3" y="4" width="18" height="16" rx="2" />
        <path d="M9 4v16" />
      </svg>
    </button>
  );
}

const SIDEBAR_KEY = 'aenotes.sidebar';

/** useSidebar is the sidebar's open state, hidden unless the visitor opened
 *  it in this browser before. */
export function useSidebar(): [boolean, () => void] {
  const [open, setOpen] = useState(() => {
    try {
      return localStorage.getItem(SIDEBAR_KEY) === 'open';
    } catch {
      return false;
    }
  });
  const toggle = () =>
    setOpen(o => {
      try {
        localStorage.setItem(SIDEBAR_KEY, o ? 'closed' : 'open');
      } catch {
        /* storage blocked */
      }
      return !o;
    });
  return [open, toggle];
}

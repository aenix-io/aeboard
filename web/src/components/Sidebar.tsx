import { type DragEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react';

import type { Board, Folder } from '../api';
import { DRAG_TYPE, type Dragged, dragData, type Mode, useCatalog } from '../catalog';
import { childrenOf, type Entry, entriesOf, pathOf, topLevel } from '../library';
import { navigate, type Route } from '../router';
import { untitled } from './BoardPage';
import { BoardIcon, FolderIcon, McpIcon, PageIcon } from './icons';
import { McpDialog } from './McpDialog';

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

/** Sidebar is the tree of folders, pages and boards, after AFFiNE's: a
 *  folder only expands and collapses; a document opens, and expands to the
 *  documents it links to — its children. "+" creates anything anywhere (in
 *  a document: a new page linked from it), dragging moves, and dropping a
 *  document on another one's middle puts it inside (links it from there). */
export function Sidebar({ route, hosted }: { route: Route; hosted: boolean }) {
  const cat = useCatalog();
  const [mcpOpen, setMcpOpen] = useState(false);
  const closeMcp = useCallback(() => setMcpOpen(false), []);
  const [expanded, setExpanded] = useState<Set<string>>(loadExpanded);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  // Where a drag would land on a row: before it, after it, or into it (a
  // folder's middle).
  // "first" is the bottom edge of an open folder: the item goes in as its
  // first entry, which is where the line under the folder's row points.
  const [dropAt, setDropAt] = useState<{ id: string; pos: DropPos } | null>(null);
  // The folder an inline "new folder" field is open in, or null.
  const [newFolderIn, setNewFolderIn] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);

  const docs = useMemo(() => cat.docs ?? [], [cat.docs]);
  // What the levels list: documents nested in another one show only there.
  const placed = useMemo(() => topLevel(cat.folders, docs), [cat.folders, docs]);
  const currentDoc = route.name === 'board' ? route.id : '';
  const currentFolder = docs.find(d => d.id === currentDoc)?.folder ?? '';

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

  const createChild = async (mode: Mode, parent: string, node: string) => {
    toggle(node, true);
    try {
      const b = await cat.createChild(mode, parent);
      navigate(`/b/${b.id}`);
    } catch {
      /* shown by the catalog's error */
    }
  };

  // The item being dragged from here: a drop target needs it while the drag
  // is over it, when the browser keeps the drag data unreadable.
  const dragging = useRef<Dragged | null>(null);
  const drag = (item: Dragged) =>
    cat.canWrite
      ? {
          draggable: true,
          onDragStart: (e: DragEvent) => {
            e.stopPropagation();
            dragging.current = item;
            e.dataTransfer.setData(DRAG_TYPE, JSON.stringify(item));
            e.dataTransfer.effectAllowed = 'move';
          },
          onDragEnd: () => {
            dragging.current = null;
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
    const entries = entriesOf(cat.folders, placed, parent);
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
        {entries.map(e =>
          e.kind === 'folder' ? folderRow(e.folder, depth, parent) : docRow(e.doc, depth, { parent, node: `d:${e.id}` }),
        )}
        {entries.length === 0 && newFolderIn !== parent && depth > 0 && (
          <div className="sb-empty" style={{ paddingLeft: indent(depth) + 22 }}>
            Empty
          </div>
        )}
      </>
    );
  };

  // rowDrop makes a row take drops: on its top or bottom edge the dragged
  // item goes before or after it (in the row's folder), on a folder's middle
  // into that folder, on an open folder's bottom edge in as its first entry,
  // on a document's middle inside that document. A document shown inside
  // another (linked: its place is the parent's text) only takes "into".
  const rowDrop = (entry: { kind: 'folder' | 'doc'; id: string }, parent: string, linked = false) => ({
    onDragOver: (e: DragEvent<HTMLDivElement>) => {
      if (!cat.canWrite || !e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      e.dataTransfer.dropEffect = 'move';
      const r = e.currentTarget.getBoundingClientRect();
      const y = (e.clientY - r.top) / r.height;
      let pos: DropPos = linked ? 'into' : y < 0.25 ? 'before' : y > 0.75 ? 'after' : 'into';
      if (pos === 'after' && entry.kind === 'folder' && expanded.has(entry.id)) pos = 'first';
      if (pos === 'into' && entry.kind === 'doc' && dragging.current && !cat.acceptsChild(dragging.current, entry.id)) {
        e.dataTransfer.dropEffect = 'none';
        setDropAt(d => (d?.id === entry.id ? null : d));
        return;
      }
      setDropAt(d => (d?.id === entry.id && d.pos === pos ? d : { id: entry.id, pos }));
    },
    onDragLeave: (e: DragEvent<HTMLDivElement>) => {
      if (e.currentTarget.contains(e.relatedTarget as Node)) return;
      setDropAt(d => (d?.id === entry.id ? null : d));
    },
    onDrop: (e: DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      e.stopPropagation();
      const at = dropAt;
      setDropAt(null);
      const item = dragData(e);
      if (!item || !at || at.id !== entry.id || (item.kind === entry.kind && item.id === entry.id)) return;
      if (at.pos === 'into' && entry.kind === 'doc') {
        if (cat.acceptsChild(item, entry.id)) void cat.addChild(entry.id, item.id).catch(() => {});
        return;
      }
      if (at.pos === 'into') {
        if (cat.accepts(item, entry.id)) {
          toggle(entry.id, true);
          void cat.move(item, entry.id).catch(() => {});
        }
        return;
      }
      if (at.pos === 'first') {
        if (item.kind === 'folder' && (entry.id === item.id || pathOf(cat.folders, entry.id).some(f => f.id === item.id))) return;
        void cat.place(item, entry.id, 0).catch(() => {});
        return;
      }
      // A folder may not go below itself.
      if (item.kind === 'folder' && (parent === item.id || pathOf(cat.folders, parent).some(f => f.id === item.id))) return;
      const siblings = entriesOf(cat.folders, placed, parent).filter(
        (x: Entry) => !(x.kind === item.kind && x.id === item.id),
      );
      const i = siblings.findIndex(x => x.kind === entry.kind && x.id === entry.id);
      void cat.place(item, parent, at.pos === 'before' ? i : i + 1).catch(() => {});
    },
    'data-drop-pos': dropAt?.id === entry.id ? dropAt.pos : undefined,
  });

  // dropLine is the line showing where a drop lands, indented to the level
  // the item will be at: the row's own for before/after, one deeper for an
  // open folder's first entry.
  const dropLine = (id: string, depth: number) => {
    if (dropAt?.id !== id || dropAt.pos === 'into') return null;
    const level = dropAt.pos === 'first' ? depth + 1 : depth;
    return (
      <span
        className={`sb-drop-line sb-drop-${dropAt.pos === 'before' ? 'top' : 'bottom'}`}
        style={{ left: indent(level) + 20 }}
        aria-hidden="true"
      />
    );
  };

  const folderRow = (f: Folder, depth: number, parent: string) => {
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
          className="sb-row"
          style={{ paddingLeft: indent(depth) }}
          {...drag({ kind: 'folder', id: f.id })}
          {...rowDrop({ kind: 'folder', id: f.id }, parent)}
        >
          {dropLine(f.id, depth)}
          <button
            type="button"
            className="sb-caret"
            aria-label={open ? `Collapse ${f.name}` : `Expand ${f.name}`}
            aria-expanded={open}
            onClick={() => toggle(f.id)}
          >
            {open ? '▾' : '▸'}
          </button>
          <button type="button" className="sb-label sb-folder-label" aria-expanded={open} onClick={() => toggle(f.id)}>
            <span className="kind kind-folder">
              <FolderIcon />
            </span>
            <span className="sb-name">{f.name}</span>
          </button>
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

  // docRow is a document and, expanded, the documents it links to. `at` is
  // where it shows: the folder level (parent) or inside another document
  // (linked); node keys its expanded state, per place it shows in.
  const docRow = (d: Board, depth: number, at: { parent: string; node: string; linked?: boolean; above?: Set<string> }) => {
    const above = at.above ?? new Set<string>();
    // A document already open above (a cycle) does not repeat below itself.
    const kids = childrenOf(docs, d).filter(c => !above.has(c.id) && c.id !== d.id);
    const open = kids.length > 0 && expanded.has(at.node);
    const label = d.title || untitled(d.mode);
    return (
      <div key={at.node} className="sb-node">
        <div
          className={`sb-row${d.id === currentDoc ? ' sb-active' : ''}`}
          style={{ paddingLeft: indent(depth) }}
          {...drag({ kind: 'doc', id: d.id })}
          {...rowDrop({ kind: 'doc', id: d.id }, at.parent, at.linked)}
        >
          {dropLine(d.id, depth)}
          {kids.length > 0 ? (
            <button
              type="button"
              className="sb-caret"
              aria-label={open ? `Collapse ${label}` : `Expand ${label}`}
              aria-expanded={open}
              onClick={() => toggle(at.node)}
            >
              {open ? '▾' : '▸'}
            </button>
          ) : (
            <span className="sb-caret sb-caret-none" />
          )}
          <a className="sb-label" href={`/b/${d.id}`} onClick={go(`/b/${d.id}`)}>
            <span className={`kind kind-${d.mode}${at.linked ? ' kind-linked' : ''}`}>
              {d.mode === 'page' ? <PageIcon /> : <BoardIcon />}
            </span>
            <span className={`sb-name${d.title ? '' : ' sb-untitled'}`}>{label}</span>
          </a>
          {cat.canWrite && (
            <span className="sb-actions">
              <RowMenu
                label="Add inside"
                glyph="+"
                items={[
                  ['New page inside', () => void createChild('page', d.id, at.node)],
                  ['New board inside', () => void createChild('edgeless', d.id, at.node)],
                ]}
              />
            </span>
          )}
        </div>
        {open &&
          kids.map(c =>
            docRow(c, depth + 1, {
              parent: d.id,
              node: `${at.node}/${c.id}`,
              linked: true,
              above: new Set([...above, d.id]),
            }),
          )}
      </div>
    );
  };

  return (
    <aside className="sidebar" aria-label="Pages and boards">
      <div className="sb-head">
        <a className="sb-home" href="/" onClick={go('/')} {...drop('')}>
          <img className="sb-logo" src="/icon-192.png" alt="" width={22} height={22} />
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
      <div className="sb-foot">
        <button type="button" className="sb-foot-button" onClick={() => setMcpOpen(true)}>
          <McpIcon />
          MCP
        </button>
      </div>
      {mcpOpen && <McpDialog hosted={hosted} onClose={closeMcp} />}
    </aside>
  );
}

const indent = (depth: number) => 8 + depth * 14;

type DropPos = 'before' | 'after' | 'into' | 'first';

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

/** useSidebar is the sidebar's open state, kept in the browser: open unless
 *  the visitor hid it here — or, on a narrow screen where it would cover the
 *  page, hidden unless they opened it. */
export function useSidebar(): [boolean, () => void] {
  const [open, setOpen] = useState(() => {
    let saved: string | null = null;
    try {
      saved = localStorage.getItem(SIDEBAR_KEY);
    } catch {
      /* storage blocked */
    }
    if (saved === 'open' || saved === 'closed') return saved === 'open';
    return !window.matchMedia('(max-width: 640px)').matches;
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

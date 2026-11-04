import { type DragEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react';

import type { Board, Folder } from '../api';
import { DRAG_TYPE, type Dragged, dragData, type Mode, useCatalog } from '../catalog';
import { childrenOf, type Entry, entriesOf, pathOf, topLevel } from '../library';
import { navigate, type Route } from '../router';
import { type Container, type Drop, levelAt, resolveDrop, type RowCtx, type Zone } from '../tree';
import { untitled } from './BoardPage';
import { BoardIcon, FolderIcon, McpIcon, PageIcon, TrashIcon } from './icons';
import { Confirm } from './Confirm';
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
  // Where a drag would land: the row under the pointer, the zone of it and
  // what that means (see resolveDrop).
  // Keyed by the row's place in the tree: a document linked from two
  // documents has two rows, and only the one under the pointer takes it.
  const [dropAt, setDropAt] = useState<{ node: string; zone: Zone; drop: Drop } | null>(null);
  // The folder an inline "new folder" field is open in, or null.
  const [newFolderIn, setNewFolderIn] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  // The row last clicked to open a document: a document linked from two
  // places has two rows, and only the one used is marked as open.
  const [opened, setOpened] = useState<{ doc: string; node: string } | null>(null);
  // The document a "move to trash" confirmation is open for.
  const [trashing, setTrashing] = useState<Board | null>(null);

  const docs = useMemo(() => cat.docs ?? [], [cat.docs]);
  // What the levels list: documents nested in another one show only there.
  const placed = useMemo(() => topLevel(cat.folders, docs), [cat.folders, docs]);
  const currentDoc = route.name === 'board' ? route.id : '';
  const currentFolder = docs.find(d => d.id === currentDoc)?.folder ?? '';

  // Open the way down to where the visitor is.
  useEffect(() => {
    const up = pathOf(cat.folders, currentFolder).map(f => `f:${f.id}`);
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
    if (folder) toggle(`f:${folder}`, true);
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
        if (target) toggle(`f:${target}`, true);
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

  // renderLevel draws a folder's contents ("" is the top level); up are the
  // rows it is under, for drops that take an item out to their levels.
  const renderLevel = (parent: string, depth: number, up: RowCtx[]): ReactNode => {
    const entries = entriesOf(cat.folders, placed, parent);
    const container: Container = { kind: 'folder', id: parent };
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
        {entries.map((e, i) => {
          const last = i === entries.length - 1;
          return e.kind === 'folder'
            ? folderRow(e.folder, { node: `f:${e.id}`, container, depth, last, up })
            : docRow(e.doc, { node: `${parent ? `f:${parent}` : ''}/d:${e.id}`, container, depth, last, up });
        })}
        {entries.length === 0 && newFolderIn !== parent && depth > 0 && (
          <div className="sb-empty" style={{ paddingLeft: indent(depth) + 22 }}>
            Empty
          </div>
        )}
      </>
    );
  };

  // accepts reports whether item may land where drop says.
  const accepts = (item: Dragged | null, d: Drop): boolean => {
    if (!item || !cat.canWrite) return false;
    if (d.container.kind === 'doc') return cat.acceptsChild(item, d.container.id);
    // A folder may not go below itself.
    if (item.kind === 'folder') {
      const at = d.container.id;
      return at !== item.id && !pathOf(cat.folders, at).some(f => f.id === item.id);
    }
    return true;
  };

  // land carries out a drop.
  const land = (item: Dragged, d: Drop, row: RowCtx) => {
    // Next to itself is where it already is.
    if ((d.before ?? d.after) === item.id) return;
    // Open the row it goes into, so the item shows where it went.
    if (d.container.id === row.id) toggle(row.node, true);
    if (d.container.kind === 'doc') {
      void cat.placeChild(item, d.container.id, { before: d.before, after: d.after, first: d.first }).catch(() => {});
      return;
    }
    const folder = d.container.id;
    const siblings = entriesOf(cat.folders, placed, folder).filter(
      (x: Entry) => !(x.kind === item.kind && x.id === item.id),
    );
    const anchor = d.before ?? d.after;
    let index = siblings.length;
    if (d.first) index = 0;
    else if (anchor) {
      const k = siblings.findIndex(x => x.id === anchor);
      index = k < 0 ? siblings.length : d.after ? k + 1 : k;
    }
    void cat.place(item, folder, index).catch(() => {});
  };

  // rowDrop makes a row take drops (see resolveDrop): its top edge puts the
  // item before it, its bottom edge after it (first inside it when it is
  // open; out to an outer level, picked by the pointer, below a level's
  // last row), its middle inside it.
  const rowDrop = (row: RowCtx) => ({
    onDragOver: (e: DragEvent<HTMLDivElement>) => {
      if (!cat.canWrite || !e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      const y = (e.clientY - r.top) / r.height;
      const zone: Zone = y < 0.25 ? 'before' : y > 0.75 ? 'after' : 'into';
      const d = resolveDrop(row, zone, levelAt(e.clientX, r.left, indent, row.depth));
      const item = dragging.current;
      if (item && (!accepts(item, d) || (item.kind === row.kind && item.id === row.id && zone === 'into'))) {
        e.dataTransfer.dropEffect = 'none';
        setDropAt(x => (x?.node === row.node ? null : x));
        return;
      }
      e.dataTransfer.dropEffect = 'move';
      setDropAt(x => (x?.node === row.node && x.zone === zone && x.drop.level === d.level ? x : { node: row.node, zone, drop: d }));
    },
    onDragLeave: (e: DragEvent<HTMLDivElement>) => {
      if (e.currentTarget.contains(e.relatedTarget as Node)) return;
      setDropAt(x => (x?.node === row.node ? null : x));
    },
    onDrop: (e: DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      e.stopPropagation();
      const at = dropAt;
      setDropAt(null);
      const item = dragData(e);
      if (!item || !at || at.node !== row.node || !accepts(item, at.drop)) return;
      if (item.kind === row.kind && item.id === row.id && !at.drop.before && !at.drop.after) return;
      land(item, at.drop, row);
    },
    'data-drop-pos': dropAt?.node === row.node && dropAt.zone === 'into' ? 'into' : undefined,
  });

  // dropLine is the line showing where a drop lands, indented to the level
  // the item will be at.
  const dropLine = (row: RowCtx) => {
    if (dropAt?.node !== row.node || dropAt.zone === 'into') return null;
    return (
      <span
        className={`sb-drop-line sb-drop-${dropAt.zone === 'before' ? 'top' : 'bottom'}`}
        style={{ left: indent(dropAt.drop.level) + 20 }}
        aria-hidden="true"
      />
    );
  };

  type Place = { node: string; container: Container; depth: number; last: boolean; up: RowCtx[] };

  const folderRow = (f: Folder, at: Place) => {
    const open = expanded.has(at.node);
    if (renaming === f.id) {
      return (
        <NameRow
          key={at.node}
          depth={at.depth}
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
    const row: RowCtx = { kind: 'folder', id: f.id, node: at.node, container: at.container, depth: at.depth, last: at.last, open, up: at.up };
    return (
      <div key={at.node} className="sb-node">
        <div
          className="sb-row"
          style={{ paddingLeft: indent(at.depth) }}
          {...drag({ kind: 'folder', id: f.id })}
          {...rowDrop(row)}
        >
          {dropLine(row)}
          <button
            type="button"
            className="sb-caret"
            aria-label={open ? `Collapse ${f.name}` : `Expand ${f.name}`}
            aria-expanded={open}
            onClick={() => toggle(at.node)}
          >
            {open ? '▾' : '▸'}
          </button>
          <button type="button" className="sb-label sb-folder-label" aria-expanded={open} onClick={() => toggle(at.node)}>
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
                      toggle(at.node, true);
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
        {open && renderLevel(f.id, at.depth + 1, [...at.up, row])}
      </div>
    );
  };

  // The open document's row: the one clicked, or (opened another way: a
  // link, the list) its first row in the tree. Rows render in tree order,
  // so the first one to ask takes it.
  let firstTaken = false;
  const isOpenRow = (doc: string, node: string) => {
    if (doc !== currentDoc) return false;
    if (opened?.doc === doc) return opened.node === node;
    if (firstTaken) return false;
    firstTaken = true;
    return true;
  };

  // docRow is a document and, expanded, the documents it links to. A
  // document shows wherever it is linked from, so a row is keyed by its
  // path (node), not by the document.
  const docRow = (d: Board, at: Place) => {
    const above = new Set(at.up.filter(r => r.kind === 'doc').map(r => r.id));
    // A document already open above (a cycle) does not repeat below itself.
    const kids = childrenOf(docs, d).filter(c => !above.has(c.id) && c.id !== d.id);
    const open = kids.length > 0 && expanded.has(at.node);
    const label = d.title || untitled(d.mode);
    const linked = at.container.kind === 'doc';
    const row: RowCtx = { kind: 'doc', id: d.id, node: at.node, container: at.container, depth: at.depth, last: at.last, open, up: at.up };
    return (
      <div key={at.node} className="sb-node">
        <div
          className={`sb-row${isOpenRow(d.id, at.node) ? ' sb-active' : ''}`}
          style={{ paddingLeft: indent(at.depth) }}
          {...drag({ kind: 'doc', id: d.id, from: linked ? at.container.id : undefined })}
          {...rowDrop(row)}
        >
          {dropLine(row)}
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
          <a
            className="sb-label"
            href={`/b/${d.id}`}
            onClick={e => {
              setOpened({ doc: d.id, node: at.node });
              go(`/b/${d.id}`)(e);
            }}
          >
            <span className={`kind kind-${d.mode}${linked ? ' kind-linked' : ''}`}>
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
              <RowMenu label="Document actions" glyph="⋯" items={[['Move to trash', () => setTrashing(d)]]} />
            </span>
          )}
        </div>
        {open &&
          kids.map((c, i) =>
            docRow(c, {
              node: `${at.node}/${c.id}`,
              container: { kind: 'doc', id: d.id },
              depth: at.depth + 1,
              last: i === kids.length - 1,
              up: [...at.up, row],
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
        {cat.docs === null ? <div className="sb-empty">Loading…</div> : renderLevel('', 0, [])}
        {/* The space below the tree takes drops to the top level. */}
        <div className="sb-rest" {...drop('')} />
      </div>
      {cat.error && <div className="sb-error error">{cat.error}</div>}
      <div className="sb-foot">
        <a
          className={`sb-foot-button${route.name === 'trash' ? ' sb-active' : ''}`}
          href="/trash"
          onClick={go('/trash')}
        >
          <TrashIcon />
          Trash
        </a>
        <button type="button" className="sb-foot-button" onClick={() => setMcpOpen(true)}>
          <McpIcon />
          MCP
        </button>
      </div>
      {mcpOpen && <McpDialog hosted={hosted} onClose={closeMcp} />}
      {trashing && (
        <Confirm
          title="Move to trash?"
          body={`${trashing.title || untitled(trashing.mode)} will be moved to the trash. You can restore it from there.`}
          action="Move to trash"
          danger
          onCancel={() => setTrashing(null)}
          onConfirm={() => {
            const id = trashing.id;
            setTrashing(null);
            void cat.trashDoc(id).catch(() => {});
          }}
        />
      )}
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

import { type DragEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react';

import type { Board, Folder } from '../api';
import { DRAG_TYPE, type Dragged, dragData, type Mode, useCatalog } from '../catalog';
import { atFolderLevel, type Crumb, type Entry, entriesOf, homePath, inside, isInside, pathOf } from '../library';
import { navigate, type Route } from '../router';
import { type Container, type Drop, levelAt, resolveDrop, type RowCtx, type Zone } from '../tree';
import { untitled } from './BoardPage';
import {
  BoardIcon,
  Chevron,
  DocsIcon,
  FolderIcon,
  LinkedBoardShortcutIcon,
  LinkedPageShortcutIcon,
  McpIcon,
  MenuIcons,
  MoreIcon,
  PageIcon,
  PlusIcon,
  SearchIcon,
  SidebarIcon,
  StarIcon,
  TrashIcon,
} from './icons';
import { openSearch, toggleFavorite, useFavorites } from '../prefs';
import { Confirm } from './Confirm';
import { McpDialog } from './McpDialog';
import { type MenuItem, RowMenu } from './RowMenu';

const OPEN_KEY = 'aenotes.sidebar.expanded';
const FOLDED_KEY = 'aenotes.sidebar.folded';

function loadSet(key: string): Set<string> {
  try {
    const v = JSON.parse(localStorage.getItem(key) ?? '[]');
    return new Set(Array.isArray(v) ? (v as string[]) : []);
  } catch {
    return new Set();
  }
}

function saveSet(key: string, s: Set<string>) {
  try {
    localStorage.setItem(key, JSON.stringify([...s]));
  } catch {
    /* storage blocked */
  }
}

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

/** Sidebar is the tree of folders, pages and boards. Every document has one
 *  place — a folder, the top level or inside another document — and shows
 *  there; the documents a document links to that live elsewhere show inside
 *  it as shortcuts (with an arrow). A folder only expands and collapses; a
 *  document opens. "+" creates anything anywhere, dragging moves a document
 *  (or a shortcut, which is a link), and a shortcut's menu can make it the
 *  original or a separate copy. */
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
  const favorites = useFavorites();
  // The sidebar's sections, folded or not (remembered in the browser).
  const [folded, setFolded] = useState<Set<string>>(() => loadSet(FOLDED_KEY));
  const fold = (name: string) => {
    const next = new Set(folded);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    setFolded(next);
    saveSet(FOLDED_KEY, next);
  };
  // The document a "move to trash" confirmation is open for.
  const [trashing, setTrashing] = useState<Board | null>(null);

  const docs = useMemo(() => cat.docs ?? [], [cat.docs]);
  // What the folder levels list: documents living in another one show there.
  const placed = useMemo(() => atFolderLevel(docs), [docs]);
  const currentDoc = route.name === 'board' ? route.id : '';
  // The row of the open document: the one clicked, or its own place.
  const trail = cat.trail?.doc === currentDoc ? cat.trail : null;
  const homeKey = useMemo(() => homeNodes(homePath(cat.folders, docs, currentDoc), currentDoc), [cat.folders, docs, currentDoc]);

  // Open the way down to where the visitor is (the document's own place,
  // when they did not come by a row).
  useEffect(() => {
    if (!currentDoc || trail) return;
    const up = homeKey.slice(0, -1);
    if (up.every(id => expanded.has(id))) return;
    const next = new Set(expanded);
    up.forEach(id => next.add(id));
    setExpanded(next);
    saveExpanded(next);
  }, [homeKey.join(' '), trail]); // eslint-disable-line react-hooks/exhaustive-deps

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

  // The rows a level shows under document d: its own documents, then its
  // shortcuts.
  const rowsIn = (d: Board) => {
    const { docs: own, shortcuts } = inside(docs, d);
    return [...own.map(x => ({ doc: x, shortcut: false })), ...shortcuts.map(x => ({ doc: x, shortcut: true }))];
  };
  const isShortcutIn = (parent: string, id: string) => {
    const p = docs.find(d => d.id === parent);
    return !!p && inside(docs, p).shortcuts.some(x => x.id === id);
  };

  // accepts reports whether item may land where drop says.
  const accepts = (item: Dragged | null, d: Drop): boolean => {
    if (!item || !cat.canWrite) return false;
    if (d.container.kind === 'doc') {
      const at = d.container.id;
      if (item.kind !== 'doc' || item.id === at) return false;
      // A shortcut is a link, anywhere; a document may not go inside itself.
      return item.shortcut || !isInside(docs, at, item.id);
    }
    // Folders hold documents and folders, not shortcuts.
    if (item.shortcut) return false;
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
    const anchor = d.before ?? d.after;
    if (d.container.kind === 'doc') {
      const parent = d.container.id;
      const anchorIsShortcut = !!anchor && isShortcutIn(parent, anchor);
      if (item.shortcut) {
        // Among the shortcuts; next to one of the documents it goes first.
        const at = anchorIsShortcut ? { before: d.before, after: d.after } : anchor ? { first: true } : {};
        void cat.placeShortcut(item, parent, at).catch(() => {});
        return;
      }
      const p = docs.find(x => x.id === parent);
      const own = p ? inside(docs, p).docs.filter(x => x.id !== item.id) : [];
      let index = own.length;
      if (d.first) index = 0;
      else if (anchor && !anchorIsShortcut) {
        const k = own.findIndex(x => x.id === anchor);
        if (k >= 0) index = d.after ? k + 1 : k;
      }
      void cat.placeInDoc(item, parent, index).catch(() => {});
      return;
    }
    const folder = d.container.id;
    const siblings = entriesOf(cat.folders, placed, folder).filter(
      (x: Entry) => !(x.kind === item.kind && x.id === item.id),
    );
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
  const rowDrop = (row: RowCtx, takesInto = true) => ({
    onDragOver: (e: DragEvent<HTMLDivElement>) => {
      if (!cat.canWrite || !e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      const y = (e.clientY - r.top) / r.height;
      let zone: Zone = y < 0.25 ? 'before' : y > 0.75 ? 'after' : 'into';
      if (zone === 'into' && !takesInto) zone = y < 0.5 ? 'before' : 'after';
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

  const caret = (open: boolean, label: string, onClick: () => void) => (
    <button
      type="button"
      className="sb-caret"
      aria-label={open ? `Collapse ${label}` : `Expand ${label}`}
      aria-expanded={open}
      onClick={onClick}
    >
      <Chevron open={open} />
    </button>
  );

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
          {caret(open, f.name, () => toggle(at.node))}
          <button type="button" className="sb-label sb-folder-label" aria-expanded={open} onClick={() => toggle(at.node)}>
            <span className="kind kind-folder">
              <FolderIcon open={open} />
            </span>
            <span className="sb-name">{f.name}</span>
          </button>
          {cat.canWrite && (
            <span className="sb-actions">
              <AddButton label={`New page in ${f.name}`} onClick={() => void createDoc('page', f.id)} />
              <RowMenu
                label="Folder actions"
                glyph={<MoreIcon />}
                items={[
                  ['Rename', () => setRenaming(f.id), <MenuIcons.rename key="i" />],
                  [
                    'Create a subfolder',
                    () => {
                      toggle(at.node, true);
                      setNewFolderIn(f.id);
                    },
                    <MenuIcons.folder key="i" />,
                  ],
                  ['New board', () => void createDoc('edgeless', f.id), <MenuIcons.board key="i" />],
                  ['Delete', () => void cat.deleteFolder(f.id).catch(() => {}), <MenuIcons.trash key="i" />],
                ]}
              />
            </span>
          )}
        </div>
        {open && renderLevel(f.id, at.depth + 1, [...at.up, row])}
      </div>
    );
  };

  // The open document's row: the one clicked, or its own place.
  const isOpenRow = (doc: string, node: string, shortcut: boolean) => {
    if (doc !== currentDoc) return false;
    return trail ? trail.node === node : !shortcut;
  };

  // crumbsOf is the way down to a row, for the header's path.
  const crumbsOf = (up: RowCtx[]): Crumb[] =>
    up.map(r => ({
      kind: r.kind,
      id: r.id,
      name:
        r.kind === 'folder'
          ? (cat.folders.find(f => f.id === r.id)?.name ?? '')
          : (docs.find(x => x.id === r.id)?.title ?? ''),
    }));

  // docRow is a document in its own place and, expanded, what is inside it:
  // the documents living there, then shortcuts. A shortcut row (a link to a
  // document living elsewhere) has an arrow and does not expand.
  const docRow = (d: Board, at: Place, shortcut = false, fav = false) => {
    const kids = shortcut ? [] : rowsIn(d);
    const open = kids.length > 0 && expanded.has(at.node);
    const label = d.title || untitled(d.mode);
    const from = at.container.kind === 'doc' ? at.container.id : undefined;
    const row: RowCtx = { kind: 'doc', id: d.id, node: at.node, container: at.container, depth: at.depth, last: at.last, open, up: at.up };
    // A shortcut to a document the row is inside (its parent, or further up)
    // cannot become the original: the document would go inside itself.
    const canBeOriginal = !!from && from !== d.id && !isInside(docs, from, d.id);
    const isFavorite = favorites.includes(d.id);
    const favoriteItem: MenuItem = [
      isFavorite ? 'Remove from favorites' : 'Add to favorites',
      () => toggleFavorite(d.id),
      isFavorite ? <MenuIcons.favorited key="i" /> : <MenuIcons.favorite key="i" />,
    ];
    // What a reader may do comes first: the rest is cut for read-only.
    const items: MenuItem[] = shortcut
      ? [
          [
            'Go to original',
            () => {
              // Opened as itself, not from this row: the original's row
              // shows open, and the path is to where it lives.
              cat.setTrail(null);
              navigate(`/b/${d.id}`);
            },
            <MenuIcons.original key="i" />,
          ],
          favoriteItem,
          ...(canBeOriginal
            ? [
                [
                  'Make this the original',
                  () => from && void cat.makeOriginal(d.id, from).catch(() => {}),
                  <MenuIcons.page key="i" />,
                ] as MenuItem,
              ]
            : []),
          [
            'Make a separate copy',
            () =>
              from &&
              void cat
                .copyShortcut(d.id, from)
                .then(b => navigate(`/b/${b.id}`))
                .catch(() => {}),
            <MenuIcons.copy key="i" />,
          ],
          ['Remove shortcut', () => from && void cat.removeShortcut(d.id, from).catch(() => {}), <MenuIcons.unlink key="i" />],
        ]
      : [
          favoriteItem,
          ['New board inside', () => void createChild('edgeless', d.id, at.node), <MenuIcons.board key="i" />],
          ['Move to trash', () => setTrashing(d), <MenuIcons.trash key="i" />],
        ];
    const readable = shortcut ? 2 : 1;
    return (
      <div key={at.node} className="sb-node">
        <div
          className={`sb-row${isOpenRow(d.id, at.node, shortcut) ? ' sb-active' : ''}${shortcut ? ' sb-shortcut' : ''}`}
          style={{ paddingLeft: indent(at.depth) }}
          title={shortcut ? `Shortcut to ${label}` : undefined}
          {...drag({ kind: 'doc', id: d.id, from, shortcut })}
          {...(fav ? {} : rowDrop(row, !shortcut))}
        >
          {dropLine(row)}
          {kids.length > 0 ? caret(open, label, () => toggle(at.node)) : <span className="sb-caret sb-caret-none" />}
          <a
            className="sb-label"
            href={`/b/${d.id}`}
            onClick={e => {
              cat.setTrail({ doc: d.id, node: at.node, path: crumbsOf(at.up), shortcut });
              go(`/b/${d.id}`)(e);
            }}
          >
            <span className={`kind kind-${d.mode}`}>
              {shortcut ? (
                d.mode === 'page' ? <LinkedPageShortcutIcon /> : <LinkedBoardShortcutIcon />
              ) : d.mode === 'page' ? (
                <PageIcon />
              ) : (
                <BoardIcon />
              )}
            </span>
            <span className={`sb-name${d.title ? '' : ' sb-untitled'}`}>{label}</span>
          </a>
          {(cat.canWrite || !shortcut) && (
            <span className="sb-actions">
              {!shortcut && cat.canWrite && (
                <AddButton label={`New page in ${label}`} onClick={() => void createChild('page', d.id, at.node)} />
              )}
              <RowMenu
                label={shortcut ? 'Shortcut actions' : 'Document actions'}
                glyph={<MoreIcon />}
                items={cat.canWrite ? items : items.slice(0, readable)}
              />
            </span>
          )}
        </div>
        {open &&
          kids.map((k, i) =>
            docRow(
              k.doc,
              {
                node: `${at.node}/${k.shortcut ? 's:' : ''}${k.doc.id}`,
                container: { kind: 'doc', id: d.id },
                depth: at.depth + 1,
                last: i === kids.length - 1,
                up: [...at.up, row],
              },
              k.shortcut,
            ),
          )}
      </div>
    );
  };

  const favDocs = favorites.flatMap(id => docs.filter(d => d.id === id));
  const section = (name: string, extra?: ReactNode) => (
    <div className="sb-section">
      <button type="button" className="sb-section-title" aria-expanded={!folded.has(name)} onClick={() => fold(name)}>
        {name}
        <Chevron open={!folded.has(name)} size={14} />
      </button>
      {extra}
    </div>
  );

  return (
    <aside className="sidebar" aria-label="Pages and boards">
      {/* The head row holds the sidebar's button (fixed in the corner). */}
      <div className="sb-head" />
      <nav className="sb-nav">
        <button type="button" className="sb-nav-item" onClick={openSearch}>
          <SearchIcon />
          Search
          <kbd className="sb-kbd">⌘K</kbd>
        </button>
        <a className={`sb-nav-item${route.name === 'list' ? ' sb-active' : ''}`} href="/" onClick={go('/')}>
          <DocsIcon />
          All documents
        </a>
      </nav>
      <div className="sb-tree">
        {section('Favorites')}
        {!folded.has('Favorites') &&
          (favDocs.length === 0 ? (
            <div className="sb-fav-empty">
              <span className="sb-fav-empty-icon">
                <StarIcon on={false} />
              </span>
              No favorites
              <span className="sb-fav-empty-hint">Star a document to keep it here.</span>
            </div>
          ) : (
            favDocs.map((d, i) =>
              docRow(d, { node: `fav/d:${d.id}`, container: { kind: 'folder', id: '' }, depth: 0, last: i === favDocs.length - 1, up: [] }, false, true),
            )
          ))}
        {section(
          'Organize',
          cat.canWrite && (
            <span className="sb-section-actions">
              <AddButton label="New page" onClick={() => void createDoc('page', '')} />
              <RowMenu
                label="More to add"
                glyph={<MoreIcon />}
                items={[
                  ['New board', () => void createDoc('edgeless', ''), <MenuIcons.board key="i" />],
                  ['New folder', () => setNewFolderIn(''), <MenuIcons.folder key="i" />],
                ]}
              />
            </span>
          ),
        )}
        {!folded.has('Organize') && (
          <>
            {cat.docs === null ? <div className="sb-empty">Loading…</div> : renderLevel('', 0, [])}
            {/* The space below the tree takes drops to the top level. */}
            <div className="sb-rest" {...drop('')} />
          </>
        )}
        {section('Others')}
        {!folded.has('Others') && (
          <>
            <a className={`sb-nav-item${route.name === 'trash' ? ' sb-active' : ''}`} href="/trash" onClick={go('/trash')}>
              <TrashIcon />
              Trash
            </a>
            <button type="button" className="sb-nav-item" onClick={() => setMcpOpen(true)}>
              <McpIcon />
              Connect an agent (MCP)
            </button>
          </>
        )}
      </div>
      {cat.error && <div className="sb-error error">{cat.error}</div>}
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
      <SidebarIcon />
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

/** homeNodes are the row keys down to a document's own place (folders, the
 *  documents it is inside, and its own row last). */
function homeNodes(path: Crumb[], doc: string): string[] {
  const out: string[] = [];
  let folder = '';
  let node = '';
  for (const c of path) {
    if (c.kind === 'folder') {
      folder = c.id;
      out.push(`f:${c.id}`);
      continue;
    }
    node = node ? `${node}/${c.id}` : `${folder ? `f:${folder}` : ''}/d:${c.id}`;
    out.push(node);
  }
  out.push(node ? `${node}/${doc}` : `${folder ? `f:${folder}` : ''}/d:${doc}`);
  return out;
}

/** AddButton is a row's "+": a new Untitled page right there. */
function AddButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      className="sb-icon-button"
      aria-label={label}
      title={label}
      onClick={e => {
        e.preventDefault();
        e.stopPropagation();
        onClick();
      }}
    >
      <PlusIcon />
    </button>
  );
}

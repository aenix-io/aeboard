import { type ReactNode, useEffect, useRef, useState } from 'react';

import type { DocSession } from '../board/editor';
import { useCatalog } from '../catalog';

import { api, type Board } from '../api';
import { homePath, isInside } from '../library';
import { navigate } from '../router';
import { noteOpened, toggleFavorite, useFavorites } from '../prefs';
import { MoreIcon, StarIcon } from './icons';
import { RowMenu } from './RowMenu';
import { Confirm } from './Confirm';

interface Props {
  id: string;
  userName: string;
  /** False for a visitor with read access: the title and the board itself
   *  are shown, not edited (the server drops their writes anyway). */
  canWrite: boolean;
  /** The sidebar toggle, first in the header. */
  lead?: ReactNode;
  whoami: ReactNode;
  /** The sync badge, left of the Board/Page switch. */
  status?: ReactNode;
}

type View = 'edgeless' | 'page';

function viewFromURL(): View | undefined {
  const v = new URLSearchParams(location.search).get('view');
  return v === 'page' || v === 'edgeless' ? v : undefined;
}

/** DocPath is the header's way down to the document: the folders and
 *  documents above the row the visitor opened it from in the sidebar, or
 *  above its own place. */
function DocPath({ id }: { id: string }) {
  const cat = useCatalog();
  const trail = cat.trail?.doc === id ? cat.trail : null;
  const path = trail ? trail.path : homePath(cat.folders, cat.docs ?? [], id);
  const open = (doc: string) => (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    cat.setTrail(null);
    navigate(`/b/${doc}`);
  };
  if (path.length === 0) return null;
  return (
    <nav className="doc-crumbs" aria-label="Path">
      {path.map(c => (
        <span key={`${c.kind}:${c.id}`} className="crumb-sep-wrap">
          {c.kind === 'doc' ? (
            <a href={`/b/${c.id}`} onClick={open(c.id)}>
              {c.name || 'Untitled'}
            </a>
          ) : (
            <span className="crumb-folder">{c.name}</span>
          )}
          <span className="crumb-sep"> / </span>
        </span>
      ))}
    </nav>
  );
}

/** ToOriginal is the button after the title of a document opened from a
 *  shortcut: it shows the document where it lives. */
function ToOriginal({ id }: { id: string }) {
  const cat = useCatalog();
  if (!(cat.trail?.doc === id && cat.trail.shortcut)) return null;
  return (
    <a
      className="to-original"
      href={`/b/${id}`}
      title="Show where this document lives"
      onClick={e => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        cat.setTrail(null);
      }}
    >
      ↗ Go to original
    </a>
  );
}

/** DocActions are the header's star (favorites, this browser's) and "⋯"
 *  menu: the mode the document opens in by default, and the trash. */
function DocActions({
  id,
  canWrite,
  fallbackMode,
  onTrash,
}: {
  id: string;
  canWrite: boolean;
  fallbackMode: 'edgeless' | 'page';
  onTrash: () => void;
}) {
  const cat = useCatalog();
  const favorite = useFavorites().includes(id);
  const doc = cat.docs?.find(d => d.id === id);
  const mode = doc?.mode ?? fallbackMode;
  const other = mode === 'page' ? 'edgeless' : 'page';
  const items: Array<[string, () => void]> = [];
  // Opened from a shortcut: the way to the original, and making the
  // shortcut's place the document's own (not when the shortcut's document
  // lives inside this one: it would go inside itself).
  const trail = cat.trail?.doc === id && cat.trail.shortcut ? cat.trail : null;
  const from = trail?.path.at(-1)?.kind === 'doc' ? trail.path.at(-1)!.id : '';
  if (trail) items.push(['Go to original', () => cat.setTrail(null)]);
  if (trail && canWrite && from && from !== id && !isInside(cat.docs ?? [], from, id)) {
    items.push([
      'Make this the original',
      () =>
        void cat
          .makeOriginal(id, from)
          // Now it lives where it was opened from: its own place is the path.
          .then(() => cat.setTrail(null))
          .catch(() => {}),
    ]);
  }
  if (canWrite && doc && !doc.trashed) {
    items.push([
      `Default to ${other === 'page' ? 'Page' : 'Board'} mode`,
      () => {
        cat.patchDoc(id, { mode: other });
        void api.updateBoard(id, { mode: other }).then(cat.reload, () => cat.reload());
      },
    ]);
    items.push(['Move to trash', onTrash]);
  }
  return (
    <span className="doc-actions">
      <button
        type="button"
        className={`icon-button${favorite ? ' icon-button-on' : ''}`}
        aria-pressed={favorite}
        aria-label={favorite ? 'Remove from favorites' : 'Add to favorites'}
        title={favorite ? 'Remove from favorites' : 'Add to favorites'}
        onClick={() => toggleFavorite(id)}
      >
        <StarIcon on={favorite} />
      </button>
      {items.length > 0 && <RowMenu label="Document actions" glyph={<MoreIcon />} items={items} />}
    </span>
  );
}

/** BoardPage is one document — a board or a page: a slim header (the
 *  title and the Board/Page switch, as AFFiNE's) over the editor. */
export function BoardPage({ id, userName, canWrite, lead, whoami, status }: Props) {
  const cat = useCatalog();
  const [board, setBoard] = useState<Board | null>(null);
  const [view, setView] = useState<View | undefined>(viewFromURL);
  const session = useRef<DocSession | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [title, setTitle] = useState('');
  const mount = useRef<HTMLDivElement>(null);
  const [deleting, setDeleting] = useState(false);
  const [trashing, setTrashing] = useState(false);
  useEffect(() => noteOpened(id), [id]);
  // In the trash: AFFiNE keeps such a document readable, with a way back.
  const trashed = cat.trash.some(d => d.id === id);

  useEffect(() => {
    api
      .getBoard(id)
      .then(b => {
        setBoard(b);
        setTitle(b.title);
        setView(v => v ?? b.mode);
        document.title = `${b.title || untitled(b.mode)} · aenotes`;
      })
      .catch(e => setError(String(e.message ?? e)));
    return () => {
      document.title = 'aenotes';
    };
  }, [id]);

  // The editor bundle is large; load it only when a board is opened.
  useEffect(() => {
    if (!board || !mount.current) return;
    let cancelled = false;
    let unsub: (() => void) | null = null;
    const el = mount.current;
    import('../board/editor')
      .then(({ openDoc }) =>
        openDoc(id, { name: userName }, el, {
          readonly: !canWrite,
          mode: view ?? board.mode,
          onNavigate: target => navigate(`/b/${target}`),
        }),
      )
      .then(s => {
        if (cancelled) {
          s.destroy();
          return;
        }
        session.current = s;
        (window as unknown as { aenotes: unknown }).aenotes = s;
        // The title lives in the document: follow it when it is typed in the
        // page's title line, or by someone else.
        const sub = s.workspace.slots.docListUpdated.subscribe(() => {
          const t = s.workspace.meta.getDocMeta(id)?.title ?? '';
          setTitle(t);
          setBoard(b => (b ? { ...b, title: t } : b));
          cat.patchDoc(id, { title: t }); // the sidebar and the list follow
          document.title = `${t || untitled(board.mode)} · aenotes`;
        });
        unsub = () => sub.unsubscribe();
      })
      .catch(e => setError(String(e.message ?? e)));
    return () => {
      cancelled = true;
      unsub?.();
      session.current?.destroy();
      session.current = null;
    };
    // Reconnect only when the board changes; a rename must not remount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [board?.id, userName, canWrite]);

  // The editor's own document list follows the app's (fresh from the status
  // stream): a document deleted meanwhile drops its room in the editor, so
  // no socket keeps retrying it (that holds up every other socket).
  useEffect(() => {
    if (!cat.docs) return;
    session.current?.workspace.setCatalog([...cat.docs, ...cat.trash]);
  }, [cat.docs, cat.trash]);

  const switchView = (v: View) => {
    setView(v);
    session.current?.setMode(v);
    const url = new URL(location.href);
    if (v === board?.mode) url.searchParams.delete('view');
    else url.searchParams.set('view', v);
    history.replaceState(null, '', url);
  };

  const save = async (patch: { title?: string }) => {
    try {
      const b = await api.updateBoard(id, patch);
      setBoard(b);
      setTitle(b.title);
      document.title = `${b.title} · aenotes`;
    } catch (e) {
      setError(String((e as Error).message ?? e));
    }
  };

  if (error && !board) {
    return (
      <div className="page">
        <p className="error">{error}</p>
        <button type="button" onClick={() => navigate('/')}>
          Back
        </button>
      </div>
    );
  }

  return (
    <div className="board-page">
      <header className="board-head">
        {lead}
        {board && <DocPath id={id} />}
        {board && !canWrite && (
          <>
            <h1 className="title-input title-static">{board.title || untitled(board.mode)}</h1>
          </>
        )}
        {board && canWrite && (
          <span className="title-wrap">
            {/* The hidden copy of the text sizes the field to it. */}
            <span className="title-sizer" aria-hidden="true">
              {title || untitled(board.mode)}
            </span>
            <input
              className="title-input"
              value={title}
              aria-label="Title"
              placeholder={untitled(board.mode)}
              onChange={e => setTitle(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
                if (e.key === 'Escape') {
                  setTitle(board.title);
                  (e.target as HTMLInputElement).blur();
                }
              }}
              onBlur={() => title !== board.title && save({ title })}
            />
          </span>
        )}
        {board && <ToOriginal id={id} />}
        {error && board && <span className="error">{error}</span>}
        <span className="spacer" />
        {status}
        {board && (
          <div className="segmented" role="tablist" aria-label="View">
            {(['edgeless', 'page'] as const).map(v => (
              <button
                key={v}
                type="button"
                role="tab"
                aria-selected={(view ?? board.mode) === v}
                className={`segment${(view ?? board.mode) === v ? ' segment-active' : ''}`}
                onClick={() => switchView(v)}
              >
                {v === 'edgeless' ? 'Board' : 'Page'}
              </button>
            ))}
          </div>
        )}
        {board && <DocActions id={id} canWrite={canWrite} fallbackMode={board.mode} onTrash={() => setTrashing(true)} />}
        {whoami}
      </header>
      {trashing && (
        <Confirm
          title="Move to trash?"
          body={`${title || untitled(board?.mode ?? 'page')} will be moved to the trash. You can restore it from there.`}
          action="Move to trash"
          danger
          onCancel={() => setTrashing(false)}
          onConfirm={() => {
            setTrashing(false);
            void cat.trashDoc(id).catch(() => {});
          }}
        />
      )}
      {trashed && (
        <div className="trash-banner" role="status">
          This document is in the trash.
          <span className="spacer" />
          {canWrite && (
            <>
              <button type="button" onClick={() => void cat.restoreDoc(id).catch(() => {})}>
                Restore
              </button>
              <button type="button" className="danger" onClick={() => setDeleting(true)}>
                Delete permanently
              </button>
            </>
          )}
        </div>
      )}
      {deleting && (
        <Confirm
          title="Delete permanently?"
          body={`${title || untitled(board?.mode ?? 'page')} will be deleted for good. This cannot be undone.`}
          action="Delete permanently"
          danger
          onCancel={() => setDeleting(false)}
          onConfirm={() => {
            setDeleting(false);
            void cat
              .deleteDoc(id)
              .then(() => navigate('/trash'))
              .catch(() => {});
          }}
        />
      )}
      <div className="editor-mount" ref={mount} />
    </div>
  );
}

export function untitled(mode: 'edgeless' | 'page'): string {
  return mode === 'page' ? 'Untitled page' : 'Untitled board';
}

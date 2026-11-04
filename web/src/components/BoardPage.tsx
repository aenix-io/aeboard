import { type ReactNode, useEffect, useRef, useState } from 'react';

import type { DocSession } from '../board/editor';

import { api, type Board, type Folder } from '../api';
import { pathOf } from '../library';
import { folderPath, navigate } from '../router';

interface Props {
  id: string;
  userName: string;
  /** False for a visitor with read access: the title and the board itself
   *  are shown, not edited (the server drops their writes anyway). */
  canWrite: boolean;
  whoami: ReactNode;
}

type View = 'edgeless' | 'page';

function viewFromURL(): View | undefined {
  const v = new URLSearchParams(location.search).get('view');
  return v === 'page' || v === 'edgeless' ? v : undefined;
}

/** BoardPage is one document — a board or a page: a slim header (title,
 *  its folder, the Board/Page switch) over the editor. */
export function BoardPage({ id, userName, canWrite, whoami }: Props) {
  const [board, setBoard] = useState<Board | null>(null);
  const [view, setView] = useState<View | undefined>(viewFromURL);
  const session = useRef<DocSession | null>(null);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [title, setTitle] = useState('');
  const mount = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api
      .getBoard(id)
      .then(b => {
        setBoard(b);
        setTitle(b.title);
        setView(v => v ?? b.mode);
        document.title = `${b.title || untitled(b.mode)} · aeboard`;
      })
      .catch(e => setError(String(e.message ?? e)));
    api
      .listBoards()
      .then(l => setFolders(l.folders ?? []))
      .catch(() => {});
    return () => {
      document.title = 'aeboard';
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
        (window as unknown as { aeboard: unknown }).aeboard = s;
        // The title lives in the document: follow it when it is typed in the
        // page's title line, or by someone else.
        const sub = s.workspace.slots.docListUpdated.subscribe(() => {
          const t = s.workspace.meta.getDocMeta(id)?.title ?? '';
          setTitle(t);
          setBoard(b => (b ? { ...b, title: t } : b));
          document.title = `${t || untitled(board.mode)} · aeboard`;
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
      document.title = `${b.title} · aeboard`;
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
        <a
          href={folderPath(board?.folder ?? '')}
          className="back"
          onClick={e => {
            e.preventDefault();
            navigate(folderPath(board?.folder ?? ''));
          }}
        >
          ←
        </a>
        <nav className="crumbs crumbs-small" aria-label="Folder">
          <a
            href="/"
            className="crumb"
            onClick={e => {
              e.preventDefault();
              navigate('/');
            }}
          >
            All
          </a>
          {board &&
            pathOf(folders, board.folder).map(f => (
              <span key={f.id} className="crumb-sep-wrap">
                <span className="crumb-sep">/</span>
                <a
                  href={folderPath(f.id)}
                  className="crumb"
                  onClick={e => {
                    e.preventDefault();
                    navigate(folderPath(f.id));
                  }}
                >
                  {f.name}
                </a>
              </span>
            ))}
          <span className="crumb-sep">/</span>
        </nav>
        {board && !canWrite && (
          <>
            <h1 className="title-input title-static">{board.title || untitled(board.mode)}</h1>
          </>
        )}
        {board && canWrite && (
          <>
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
          </>
        )}
        {error && board && <span className="error">{error}</span>}
        <span className="spacer" />
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
        {whoami}
      </header>
      <div className="editor-mount" ref={mount} />
    </div>
  );
}

export function untitled(mode: 'edgeless' | 'page'): string {
  return mode === 'page' ? 'Untitled page' : 'Untitled board';
}

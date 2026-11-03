import { type ReactNode, useEffect, useRef, useState } from 'react';

import { api, type Board } from '../api';
import { navigate } from '../router';
import { TagEditor } from './TagEditor';

interface Props {
  id: string;
  userName: string;
  whoami: ReactNode;
}

/** BoardPage is one board: a slim header (title, tags) over the editor. */
export function BoardPage({ id, userName, whoami }: Props) {
  const [board, setBoard] = useState<Board | null>(null);
  const [known, setKnown] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [title, setTitle] = useState('');
  const mount = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api
      .getBoard(id)
      .then(b => {
        setBoard(b);
        setTitle(b.title);
        document.title = `${b.title} · aeboard`;
      })
      .catch(e => setError(String(e.message ?? e)));
    api
      .listBoards()
      .then(l => setKnown(l.tags))
      .catch(() => {});
    return () => {
      document.title = 'aeboard';
    };
  }, [id]);

  // The editor bundle is large; load it only when a board is opened.
  useEffect(() => {
    if (!board || !mount.current) return;
    let session: { destroy(): void } | null = null;
    let cancelled = false;
    const el = mount.current;
    import('../board/editor')
      .then(({ openBoard }) => openBoard(id, { name: userName }, el))
      .then(s => {
        if (cancelled) s.destroy();
        else {
          session = s;
          (window as unknown as { aeboard: unknown }).aeboard = s;
        }
      })
      .catch(e => setError(String(e.message ?? e)));
    return () => {
      cancelled = true;
      session?.destroy();
    };
    // Reconnect only when the board changes; a rename must not remount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [board?.id, userName]);

  const save = async (patch: { title?: string; tags?: string[] }) => {
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
          Back to boards
        </button>
      </div>
    );
  }

  return (
    <div className="board-page">
      <header className="board-head">
        <a
          href="/"
          className="back"
          onClick={e => {
            e.preventDefault();
            navigate('/');
          }}
        >
          ← Boards
        </a>
        {board && (
          <>
            <input
              className="title-input"
              value={title}
              aria-label="Board title"
              onChange={e => setTitle(e.target.value)}
              onKeyDown={e => {
                if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
                if (e.key === 'Escape') {
                  setTitle(board.title);
                  (e.target as HTMLInputElement).blur();
                }
              }}
              onBlur={() => title.trim() && title !== board.title && save({ title })}
            />
            <TagEditor tags={board.tags} known={known} onChange={tags => save({ tags })} />
          </>
        )}
        {error && board && <span className="error">{error}</span>}
        <span className="spacer" />
        {whoami}
      </header>
      <div className="editor-mount" ref={mount} />
    </div>
  );
}

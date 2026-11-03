import { type FormEvent, type ReactNode, useEffect, useMemo, useState } from 'react';

import { api, type Board } from '../api';
import { navigate } from '../router';
import { filterBoards, parseTags, type TagSelection } from '../tags';
import { TagChips } from './TagChips';

const SEL_KEY = 'aeboard.tagFilter';

function loadSelection(): TagSelection {
  try {
    const v = JSON.parse(localStorage.getItem(SEL_KEY) ?? 'null');
    return Array.isArray(v) && v.length ? (v as string[]) : null;
  } catch {
    return null;
  }
}

function saveSelection(sel: TagSelection) {
  try {
    localStorage.setItem(SEL_KEY, JSON.stringify(sel));
  } catch {
    /* storage blocked */
  }
}

const when = (iso: string) => {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
};

/** BoardList is the start page: every board, a tag filter, and "New board". */
export function BoardList({ canWrite, whoami }: { canWrite: boolean; whoami: ReactNode }) {
  const [boards, setBoards] = useState<Board[] | null>(null);
  const [tags, setTags] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<TagSelection>(loadSelection);
  const [query, setQuery] = useState('');
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    api
      .listBoards()
      .then(l => {
        setBoards(l.boards);
        setTags(l.tags);
      })
      .catch(e => setError(String(e.message ?? e)));
  }, []);

  // Drop selected tags that no board carries any more.
  const selection = useMemo<TagSelection>(() => {
    if (!selected) return null;
    const live = selected.filter(t => tags.some(k => k.toLowerCase() === t.toLowerCase()));
    return live.length ? live : null;
  }, [selected, tags]);

  const select = (sel: TagSelection) => {
    setSelected(sel);
    saveSelection(sel);
  };

  const shown = boards ? filterBoards(boards, selection, query) : [];
  const empty = boards !== null && boards.length === 0;

  return (
    <div className="page">
      <header className="list-head">
        <h1>Boards</h1>
        <div className="list-tools">
          {!empty && (
            <input
              className="search"
              type="search"
              placeholder="Search boards…"
              value={query}
              onChange={e => setQuery(e.target.value)}
            />
          )}
          {canWrite && (
            <button type="button" className="primary" onClick={() => setCreating(true)}>
              New board
            </button>
          )}
          {whoami}
        </div>
      </header>
      <TagChips tags={tags} selected={selection} onSelect={select} />

      {creating && (
        <CreateBoard known={tags} preset={selection ?? []} onCancel={() => setCreating(false)} />
      )}
      {error && <p className="error">Could not load boards: {error}</p>}
      {boards === null && !error && <p className="muted">Loading…</p>}
      {empty && !creating && (
        <div className="empty">
          <p>No boards yet.</p>
          {canWrite && (
            <button type="button" className="primary" onClick={() => setCreating(true)}>
              Create the first board
            </button>
          )}
        </div>
      )}
      {boards && !empty && shown.length === 0 && <p className="muted">No boards match the filter.</p>}

      <ul className="board-grid">
        {shown.map(b => (
          <li key={b.id}>
            <a
              className="board-card"
              href={`/b/${b.id}`}
              onClick={e => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
                e.preventDefault();
                navigate(`/b/${b.id}`);
              }}
            >
              <span className="board-title">{b.title}</span>
              <span className="board-meta">Updated {when(b.updated)}</span>
              {b.tags.length > 0 && (
                <span className="chips chips-small">
                  {b.tags.map(t => (
                    <span key={t} className="chip">
                      {t}
                    </span>
                  ))}
                </span>
              )}
            </a>
          </li>
        ))}
      </ul>
    </div>
  );
}

function CreateBoard({ known, preset, onCancel }: { known: string[]; preset: string[]; onCancel: () => void }) {
  const [title, setTitle] = useState('');
  // A new board made while a filter is on carries those tags, so it shows up
  // in the list the person is looking at.
  const [tags, setTags] = useState(preset.join(', '));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const b = await api.createBoard(title, parseTags(tags));
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
        placeholder="Board title"
        value={title}
        onChange={e => setTitle(e.target.value)}
        onKeyDown={e => e.key === 'Escape' && onCancel()}
      />
      <input
        list="aeboard-create-tags"
        placeholder="Tags, comma-separated"
        value={tags}
        onChange={e => setTags(e.target.value)}
      />
      <datalist id="aeboard-create-tags">
        {known.map(t => (
          <option key={t} value={t} />
        ))}
      </datalist>
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

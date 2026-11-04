import { type FormEvent, type ReactNode, useEffect, useMemo, useState } from 'react';

import { api, type Board } from '../api';
import { navigate } from '../router';
import { filterBoards, filterKind, type KindFilter, parseTags, type TagSelection } from '../tags';
import { untitled } from './BoardPage';
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
  // The kind of document being created, or null while the form is closed.
  const [creating, setCreating] = useState<Mode | null>(null);
  const [kind, setKind] = useState<KindFilter>(loadKind);

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

  const pickKind = (k: KindFilter) => {
    setKind(k);
    saveKind(k);
  };

  const shown = boards ? filterKind(filterBoards(boards, selection, query), kind) : [];
  const empty = boards !== null && boards.length === 0;

  return (
    <div className="page">
      <header className="list-head">
        <h1>Pages and boards</h1>
        <div className="list-tools">
          {!empty && (
            <input
              className="search"
              type="search"
              placeholder="Search…"
              value={query}
              onChange={e => setQuery(e.target.value)}
            />
          )}
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
        {!empty && (
          <div className="segmented" role="tablist" aria-label="Kind">
            {KINDS.map(([k, label]) => (
              <button
                key={k}
                type="button"
                role="tab"
                aria-selected={kind === k}
                className={`segment${kind === k ? ' segment-active' : ''}`}
                onClick={() => pickKind(k)}
              >
                {label}
              </button>
            ))}
          </div>
        )}
        <TagChips tags={tags} selected={selection} onSelect={select} />
      </div>

      {creating && (
        <CreateBoard
          mode={creating}
          known={tags}
          preset={selection ?? []}
          onCancel={() => setCreating(null)}
        />
      )}
      {error && <p className="error">Could not load boards: {error}</p>}
      {boards === null && !error && <p className="muted">Loading…</p>}
      {empty && !creating && (
        <div className="empty">
          <p>Nothing here yet.</p>
          {canWrite && (
            <>
              <button type="button" className="primary" onClick={() => setCreating('page')}>
                Create the first page
              </button>{' '}
              <button type="button" onClick={() => setCreating('edgeless')}>
                or a board
              </button>
            </>
          )}
        </div>
      )}
      {boards && !empty && shown.length === 0 && <p className="muted">Nothing matches the filter.</p>}

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
              <span className="board-title">
                <span className={`kind kind-${b.mode}`} aria-label={b.mode === 'page' ? 'Page' : 'Board'}>
                  {b.mode === 'page' ? <PageIcon /> : <BoardIcon />}
                </span>
                {b.title || untitled(b.mode)}
              </span>
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

function CreateBoard({
  mode,
  known,
  preset,
  onCancel,
}: {
  mode: Mode;
  known: string[];
  preset: string[];
  onCancel: () => void;
}) {
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
      const b = await api.createBoard({ mode, title, tags: parseTags(tags) });
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

type Mode = 'edgeless' | 'page';

const KINDS: Array<[KindFilter, string]> = [
  ['all', 'All'],
  ['page', 'Pages'],
  ['edgeless', 'Boards'],
];

const KIND_KEY = 'aeboard.kindFilter';

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

function BoardIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
      <rect x="3" y="3" width="18" height="18" rx="3" />
      <path d="M7 15l3-3 3 2 4-5" />
    </svg>
  );
}

function PageIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
      <path d="M6 3h9l3 3v15H6z" />
      <path d="M9 10h6M9 14h6M9 18h4" />
    </svg>
  );
}

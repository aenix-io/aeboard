import { useEffect, useMemo, useRef, useState } from 'react';

import { api, type Board } from '../api';
import { useCatalog } from '../catalog';
import { search as byTitle } from '../library';
import { useRecent } from '../prefs';
import { navigate } from '../router';
import { untitled } from './BoardPage';
import { BoardIcon, PageIcon } from './icons';

/** docLink reads a document id out of a pasted link (…/b/<id>). */
const docLink = /(?:^|\/)b\/([0-9a-z]{10})\/?(?:[?#].*)?$/;

type Item =
  | { kind: 'doc'; doc: Board; note?: string }
  | { kind: 'go'; label: string; href: string };

/** SearchDialog is the quick finder (the sidebar's Search, ⌘K): recent
 *  documents while empty; as you type, documents by title, then by text;
 *  a pasted link to a document opens it. */
export function SearchDialog({ onClose }: { onClose: () => void }) {
  const cat = useCatalog();
  const recent = useRecent();
  const [query, setQuery] = useState('');
  const [hits, setHits] = useState<Array<{ id: string; snippet?: string }>>([]);
  const [active, setActive] = useState(0);
  const list = useRef<HTMLDivElement>(null);
  const docs = useMemo(() => cat.docs ?? [], [cat.docs]);
  const q = query.trim();

  // Text search on the server, a moment after typing stops.
  useEffect(() => {
    if (!q) {
      setHits([]);
      return;
    }
    let live = true;
    const t = setTimeout(() => {
      api.search(q).then(
        r => live && setHits(r.hits),
        () => live && setHits([]),
      );
    }, 150);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [q]);

  const sections = useMemo(() => {
    const byID = new Map(docs.map(d => [d.id, d]));
    const out: Array<{ title: string; items: Item[] }> = [];
    if (!q) {
      const items = recent.flatMap(r => {
        const d = byID.get(r.id);
        return d ? [{ kind: 'doc' as const, doc: d, note: new Date(r.at).toLocaleString() }] : [];
      });
      if (items.length) out.push({ title: 'Recent', items });
    } else {
      const link = docLink.exec(q);
      const linked = link ? byID.get(link[1]) : undefined;
      if (linked) out.push({ title: 'Link', items: [{ kind: 'doc', doc: linked }] });
      const titled = byTitle(docs, q);
      const seen = new Set(titled.map(d => d.id));
      const inText = hits.flatMap(h => {
        const d = byID.get(h.id);
        return d && !seen.has(d.id) ? [{ kind: 'doc' as const, doc: d, note: h.snippet }] : [];
      });
      const items: Item[] = [...titled.map(d => ({ kind: 'doc' as const, doc: d })), ...inText];
      if (items.length) out.push({ title: 'Documents', items });
    }
    out.push({
      title: 'Navigation',
      items: [
        { kind: 'go', label: 'Go to all documents', href: '/' },
        { kind: 'go', label: 'Go to trash', href: '/trash' },
      ],
    });
    return out;
  }, [docs, recent, hits, q]);

  const flat = sections.flatMap(s => s.items);
  useEffect(() => setActive(0), [q]);
  useEffect(() => {
    list.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const choose = (it: Item) => {
    onClose();
    if (it.kind === 'doc') {
      cat.setTrail(null);
      navigate(`/b/${it.doc.id}`);
    } else navigate(it.href);
  };

  let n = -1;
  return (
    <div className="dialog-scrim search-scrim" onClick={onClose}>
      <div className="search-dialog" role="dialog" aria-modal="true" aria-label="Search" onClick={e => e.stopPropagation()}>
        <input
          className="search-input"
          autoFocus
          placeholder="Search docs or paste link…"
          value={query}
          onChange={e => setQuery(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Escape') onClose();
            else if (e.key === 'ArrowDown') {
              e.preventDefault();
              setActive(a => Math.min(a + 1, flat.length - 1));
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              setActive(a => Math.max(a - 1, 0));
            } else if (e.key === 'Enter' && flat[active]) {
              e.preventDefault();
              choose(flat[active]);
            }
          }}
        />
        <div className="search-results" ref={list}>
          {q && flat.length === 2 && <p className="search-empty">Nothing matches.</p>}
          {sections.map(s => (
            <div key={s.title} className="search-section">
              <div className="search-section-title">{s.title}</div>
              {s.items.map(it => {
                n++;
                const i = n;
                return (
                  <button
                    key={it.kind === 'doc' ? `${s.title}:${it.doc.id}` : it.href}
                    type="button"
                    className="search-item"
                    data-active={i === active}
                    onMouseMove={() => setActive(i)}
                    onClick={() => choose(it)}
                  >
                    <span className="search-icon">
                      {it.kind === 'go' ? '→' : it.doc.mode === 'page' ? <PageIcon /> : <BoardIcon />}
                    </span>
                    <span className="search-label">{it.kind === 'doc' ? it.doc.title || untitled(it.doc.mode) : it.label}</span>
                    {it.kind === 'doc' && it.note && <span className="search-note">{it.note}</span>}
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

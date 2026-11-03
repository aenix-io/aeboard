import { useState } from 'react';

import { parseTags } from '../tags';

interface Props {
  tags: string[];
  /** Every tag in use, offered as suggestions. */
  known?: string[];
  /** Show the tags only. */
  readOnly?: boolean;
  onChange: (tags: string[]) => void;
}

/** TagEditor edits one board's tags: × removes, "+ tag" adds (comma-separated). */
export function TagEditor({ tags, known = [], readOnly, onChange }: Props) {
  const [adding, setAdding] = useState(false);
  const [value, setValue] = useState('');
  const commit = () => {
    const add = parseTags(value).filter(t => !tags.some(o => o.toLowerCase() === t.toLowerCase()));
    if (add.length) onChange([...tags, ...add]);
    setValue('');
    setAdding(false);
  };
  if (readOnly) {
    return (
      <div className="chips">
        {tags.map(t => (
          <span key={t} className="chip">
            {t}
          </span>
        ))}
      </div>
    );
  }
  return (
    <div className="chips">
      {tags.map(t => (
        <span key={t} className="chip">
          {t}
          <button
            type="button"
            className="chip-x"
            aria-label={`Remove tag ${t}`}
            onClick={() => onChange(tags.filter(o => o !== t))}
          >
            ×
          </button>
        </span>
      ))}
      {adding ? (
        <>
          <input
            className="chip-input"
            autoFocus
            list="aeboard-known-tags"
            value={value}
            placeholder="tag…"
            onChange={e => setValue(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') commit();
              if (e.key === 'Escape') {
                setValue('');
                setAdding(false);
              }
            }}
            onBlur={commit}
          />
          <datalist id="aeboard-known-tags">
            {known.map(t => (
              <option key={t} value={t} />
            ))}
          </datalist>
        </>
      ) : (
        <button type="button" className="link-button" onClick={() => setAdding(true)}>
          + tag
        </button>
      )}
    </div>
  );
}

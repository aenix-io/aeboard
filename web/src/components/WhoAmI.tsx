import { useEffect, useState } from 'react';

interface Props {
  name: string;
  onChange: (name: string) => void;
}

/** WhoAmI edits the name shown on this visitor's cursor (until sign-in lands). */
export function WhoAmI({ name, onChange }: Props) {
  const [draft, setDraft] = useState(name);
  useEffect(() => setDraft(name), [name]);
  const commit = () => {
    const n = draft.trim();
    if (n && n !== name) onChange(n);
    else setDraft(name);
  };
  return (
    <label className="whoami">
      <span>You</span>
      <input
        value={draft}
        onChange={e => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={e => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
        aria-label="Your name, shown to others on the board"
      />
    </label>
  );
}

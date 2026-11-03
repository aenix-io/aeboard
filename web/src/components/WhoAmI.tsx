import { useEffect, useState } from 'react';

import type { Identity } from '../api';

interface Props {
  /** The known identity (gh locally, the session when hosted). */
  identity: Identity | null;
  name: string;
  onChange: (name: string) => void;
}

/**
 * WhoAmI shows who this visitor is on the board: their GitHub avatar and
 * login when the server knows them, otherwise an editable display name.
 */
export function WhoAmI({ identity, name, onChange }: Props) {
  const [draft, setDraft] = useState(name);
  useEffect(() => setDraft(name), [name]);
  const commit = () => {
    const n = draft.trim();
    if (n && n !== name) onChange(n);
    else setDraft(name);
  };
  if (identity) {
    return (
      <span className="whoami" title={identity.name ? `${identity.name} (${identity.login})` : identity.login}>
        {identity.avatarUrl && <img className="avatar" src={identity.avatarUrl} alt="" width={22} height={22} />}
        <span className="whoami-login">{identity.login}</span>
      </span>
    );
  }
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

import { useEffect, useState } from 'react';

import type { Identity } from '../api';
import { SignOut } from './SignIn';

interface Props {
  /** The known identity (gh locally, the session when hosted). */
  identity: Identity | null;
  /** Signed in on a hosted server: offer "Sign out". */
  hosted?: boolean;
  /** The visitor may only look (read permission on the board repository). */
  readOnly?: boolean;
  name: string;
  onChange: (name: string) => void;
}

/**
 * WhoAmI shows who this visitor is on the board: their GitHub avatar and
 * login when the server knows them, otherwise an editable display name.
 */
export function WhoAmI({ identity, hosted, readOnly, name, onChange }: Props) {
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
        {readOnly && (
          <span className="chip chip-muted" title="You have read access to the board repository">
            view only
          </span>
        )}
        {hosted && <SignOut />}
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

// The aeboard REST API (/api/v1).

export interface Board {
  id: string;
  mode: 'edgeless' | 'page';
  title: string;
  tags: string[];
  created: string;
  updated: string;
}

export interface BoardList {
  boards: Board[];
  tags: string[];
}

/** Fired when the API answers 401: the session ended (expired, signed out
 *  elsewhere, revoked on GitHub). The app asks /me again. */
export const SIGNED_OUT = 'aeboard:signed-out';

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(`/api/v1${path}`, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (resp.status === 401) window.dispatchEvent(new Event(SIGNED_OUT));
  if (!resp.ok) {
    let msg = `${resp.status} ${resp.statusText}`;
    try {
      msg = ((await resp.json()) as { error?: string }).error ?? msg;
    } catch {
      /* not JSON */
    }
    throw new Error(msg);
  }
  return (await resp.json()) as T;
}

export interface Identity {
  login: string;
  name?: string;
  avatarUrl?: string;
}

/** What the visitor may do: "write" locally; their permission on the board
 *  repository when hosted. */
export type Access = 'none' | 'read' | 'write';

export interface Me extends Identity {
  access: Access;
  /** Signed in through the hosted mode (so there is a "Sign out"). */
  hosted?: boolean;
  /** The board repository the rights come from (hosted). */
  repo?: string;
}

/** Who the server says the visitor is. */
export type WhoResult =
  | { kind: 'known'; me: Me }
  /** Hosted, not signed in: show "Sign in with <forge>". */
  | { kind: 'signedOut'; forge: string }
  /** Local server without an identity: names are chosen in the browser. */
  | { kind: 'anonymous' };

export const api = {
  me: async (): Promise<WhoResult> => {
    const resp = await fetch('/api/v1/me');
    if (resp.ok) return { kind: 'known', me: (await resp.json()) as Me };
    if (resp.status === 401) {
      const body = (await resp.json().catch(() => ({}))) as { hosted?: boolean; forge?: string };
      if (body.hosted) return { kind: 'signedOut', forge: body.forge ?? 'GitHub' };
      return { kind: 'anonymous' };
    }
    let msg = `${resp.status} ${resp.statusText}`;
    try {
      msg = ((await resp.json()) as { error?: string }).error ?? msg;
    } catch {
      /* not JSON */
    }
    throw new Error(msg);
  },
  listBoards: () => call<BoardList>('GET', '/boards'),
  getBoard: (id: string) => call<Board>('GET', `/boards/${id}`),
  createBoard: (doc: { id?: string; mode?: 'edgeless' | 'page'; title?: string; tags?: string[]; blank?: boolean }) =>
    call<Board>('POST', '/boards', doc),
  updateBoard: (id: string, patch: { title?: string; tags?: string[] }) =>
    call<Board>('PATCH', `/boards/${id}`, patch),
};

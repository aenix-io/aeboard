// The aeboard REST API (/api/v1).

export interface Board {
  id: string;
  title: string;
  tags: string[];
  created: string;
  updated: string;
}

export interface BoardList {
  boards: Board[];
  tags: string[];
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(`/api/v1${path}`, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
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

export const api = {
  listBoards: () => call<BoardList>('GET', '/boards'),
  getBoard: (id: string) => call<Board>('GET', `/boards/${id}`),
  createBoard: (title: string, tags: string[]) => call<Board>('POST', '/boards', { title, tags }),
  updateBoard: (id: string, patch: { title?: string; tags?: string[] }) =>
    call<Board>('PATCH', `/boards/${id}`, patch),
};

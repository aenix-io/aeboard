import { useEffect, useState } from 'react';

// The routes, over the History API: "/" (the top level), "/f/<id>" (a
// folder) and "/b/<id>" (a document).
export type Route = { name: 'list'; folder: string } | { name: 'board'; id: string };

export function parseRoute(path: string): Route {
  const b = /^\/b\/([0-9a-z]+)\/?$/.exec(path);
  if (b) return { name: 'board', id: b[1] };
  const f = /^\/f\/([0-9a-z]+)\/?$/.exec(path);
  return { name: 'list', folder: f ? f[1] : '' };
}

/** folderPath is the URL of a folder ("" is the top level). */
export function folderPath(id: string): string {
  return id ? `/f/${id}` : '/';
}

export function navigate(path: string): void {
  history.pushState(null, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(location.pathname));
  useEffect(() => {
    const on = () => setRoute(parseRoute(location.pathname));
    window.addEventListener('popstate', on);
    return () => window.removeEventListener('popstate', on);
  }, []);
  return route;
}

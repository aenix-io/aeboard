import { useEffect, useState } from 'react';

// The routes, over the History API: "/" (every document), "/trash" and
// "/b/<id>" (a document). Folders have no page of their own: in the sidebar they only
// expand and collapse (AFFiNE's way), so an old /f/<id> link shows "/".
export type Route = { name: 'list' } | { name: 'trash' } | { name: 'board'; id: string };

export function parseRoute(path: string): Route {
  const b = /^\/b\/([0-9a-z]+)\/?$/.exec(path);
  if (b) return { name: 'board', id: b[1] };
  return /^\/trash\/?$/.test(path) ? { name: 'trash' } : { name: 'list' };
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

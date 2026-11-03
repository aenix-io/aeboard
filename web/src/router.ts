import { useEffect, useState } from 'react';

// A two-route router over the History API: "/" (board list) and "/b/<id>".
export type Route = { name: 'list' } | { name: 'board'; id: string };

export function parseRoute(path: string): Route {
  const m = /^\/b\/([0-9a-z]+)\/?$/.exec(path);
  return m ? { name: 'board', id: m[1] } : { name: 'list' };
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

import { useCallback, useEffect, useState } from 'react';

import { api, type Me, SIGNED_OUT, type WhoResult } from './api';

import { BoardList } from './components/BoardList';
import { BoardPage } from './components/BoardPage';
import { NoAccess, SignIn } from './components/SignIn';
import { Sidebar, SidebarToggle, useSidebar } from './components/Sidebar';
import { SearchDialog } from './components/SearchDialog';
import { TrashList } from './components/TrashList';
import { onOpenSearch } from './prefs';
import { SyncBadge } from './components/SyncBadge';
import { WhoAmI } from './components/WhoAmI';
import { CatalogProvider } from './catalog';
import { type Route, useRoute } from './router';
import { useSyncStatus } from './sync';
import { loadName, saveName } from './user';

export function App() {
  const route = useRoute();
  const [name, setName] = useState(loadName);
  // undefined while asking the server.
  const [who, setWho] = useState<WhoResult | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const ask = useCallback(() => {
    api.me().then(
      w => {
        setWho(w);
        setError(null);
      },
      e => setError(String((e as Error).message ?? e)),
    );
  }, []);
  useEffect(() => {
    ask();
    // A 401 from any call means the session is gone: ask again, which
    // lands on the sign-in screen.
    window.addEventListener(SIGNED_OUT, ask);
    return () => window.removeEventListener(SIGNED_OUT, ask);
  }, [ask]);

  if (error && !who) return <p className="error page">Could not reach the server: {error}</p>;
  if (who === undefined) return null;
  if (who.kind === 'signedOut') return <SignIn forge={who.forge} />;
  const me = who.kind === 'known' ? who.me : null;
  if (me?.access === 'none') return <NoAccess login={me.login} repo={me.repo} />;
  return <Boards me={me} name={name} onName={setName} route={route} />;
}

function Boards({
  me,
  name,
  onName,
  route,
}: {
  me: Me | null;
  name: string;
  onName: (n: string) => void;
  route: Route;
}) {
  // The status stream is behind the sign-in, so it only starts here.
  const sync = useSyncStatus();
  const [sidebar, toggleSidebar] = useSidebar();
  const [searching, setSearching] = useState(false);
  const closeSearch = useCallback(() => setSearching(false), []);
  useEffect(() => onOpenSearch(() => setSearching(true)), []);
  // ⌘K / Ctrl+K opens the finder, unless the editor took it (with text
  // selected it makes a link).
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k' && !e.defaultPrevented) {
        e.preventDefault();
        setSearching(true);
      }
    };
    window.addEventListener('keydown', on);
    return () => window.removeEventListener('keydown', on);
  }, []);
  // The sidebar's button stays in one place, the top left corner: in the
  // sidebar's head while it is open, over the page while it is not — where
  // a document's header leaves room for it (the lists' headers sit lower,
  // clear of it).
  const lead = sidebar ? null : <span className="lead-space" aria-hidden="true" />;
  const userName = me?.login ?? name;
  const canWrite = !me || me.access === 'write';
  const whoami = (
    <>
      <SyncBadge status={sync} />
      <WhoAmI
        identity={me}
        hosted={!!me?.hosted}
        readOnly={!canWrite}
        name={name}
        onChange={n => {
          onName(n);
          saveName(n);
        }}
      />
    </>
  );
  return (
    <CatalogProvider canWrite={canWrite} version={sync?.catalog}>
      <div className={`app shell${sidebar ? ' shell-sidebar' : ''}`}>
        <SidebarToggle open={sidebar} onToggle={toggleSidebar} />
        {sidebar && <Sidebar route={route} hosted={!!me?.hosted} />}
        <main className="main">
          {route.name === 'board' ? (
            <BoardPage
              key={route.id}
              id={route.id}
              userName={userName}
              canWrite={canWrite}
              lead={lead}
              whoami={whoami}
            />
          ) : route.name === 'trash' ? (
            <TrashList whoami={whoami} />
          ) : (
            <BoardList whoami={whoami} />
          )}
        </main>
      </div>
      {searching && <SearchDialog onClose={closeSearch} />}
    </CatalogProvider>
  );
}

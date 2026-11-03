import { useEffect, useState } from 'react';

import { api, type Identity } from './api';

import { BoardList } from './components/BoardList';
import { BoardPage } from './components/BoardPage';
import { WhoAmI } from './components/WhoAmI';
import { useRoute } from './router';
import { loadName, saveName } from './user';

export function App() {
  const route = useRoute();
  const [name, setName] = useState(loadName);
  // undefined while asking the server; null when anonymous.
  const [identity, setIdentity] = useState<Identity | null | undefined>(undefined);
  useEffect(() => {
    api.me().then(setIdentity, () => setIdentity(null));
  }, []);
  if (identity === undefined) return null;
  const userName = identity?.login ?? name;
  const who = (
    <WhoAmI
      identity={identity}
      name={name}
      onChange={n => {
        setName(n);
        saveName(n);
      }}
    />
  );
  return (
    <div className="app">
      {route.name === 'board' ? (
        <BoardPage key={route.id} id={route.id} userName={userName} whoami={who} />
      ) : (
        <BoardList whoami={who} />
      )}
    </div>
  );
}

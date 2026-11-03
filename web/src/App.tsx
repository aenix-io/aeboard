import { useState } from 'react';

import { BoardList } from './components/BoardList';
import { BoardPage } from './components/BoardPage';
import { WhoAmI } from './components/WhoAmI';
import { useRoute } from './router';
import { loadName, saveName } from './user';

export function App() {
  const route = useRoute();
  const [name, setName] = useState(loadName);
  const who = (
    <WhoAmI
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
        <BoardPage key={route.id} id={route.id} userName={name} whoami={who} />
      ) : (
        <BoardList whoami={who} />
      )}
    </div>
  );
}

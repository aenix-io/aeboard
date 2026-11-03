import { openBoard } from './board/editor';

const params = new URLSearchParams(location.search);
const board = params.get('board') ?? 'spike';
const name = params.get('name') ?? `guest-${Math.floor(Math.random() * 1000)}`;
const mount = document.getElementById('app')!;
openBoard(board, { name }, mount).then(s => {
  (window as unknown as { aeboard: unknown }).aeboard = s;
});

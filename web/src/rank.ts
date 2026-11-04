// Rank keys order the contents of a folder: plain strings compared
// bytewise, with always room for another key between two, so moving an item
// rewrites only that item. Ported from aeman's pkg/board/rank.go
// (RankBetween); the server checks the same shape (boards.ValidRank).
//
// One invariant makes "always room" true: a key never ends in the floor
// digit '0' — nothing fits between "a" and "a0", so "a0" is never minted.

const ALPHABET = '0123456789abcdefghijklmnopqrstuvwxyz';

const digitAt = (s: string, i: number, def: number) => (i >= s.length ? def : ALPHABET.indexOf(s[i]));

// afterTail is a suffix that sorts after prev.slice(from), as short as it can.
function afterTail(prev: string, from: number): string {
  let out = '';
  for (let i = from; ; i++) {
    if (i >= prev.length) return out + ALPHABET[ALPHABET.length >> 1];
    const d = ALPHABET.indexOf(prev[i]);
    if (d < ALPHABET.length - 1) return out + ALPHABET[(d + ALPHABET.length) >> 1];
    out += prev[i];
  }
}

/** rankBetween returns a key strictly between prev and next ("" for an
 *  open end; both empty gives a list's first key). */
export function rankBetween(prev: string, next: string): string {
  if (next !== '' && prev >= next) throw new Error(`rank: ${JSON.stringify(prev)} is not before ${JSON.stringify(next)}`);
  let out = '';
  for (let i = 0; ; i++) {
    const lo = digitAt(prev, i, 0);
    const hi = digitAt(next, i, ALPHABET.length);
    if (hi - lo > 1) return out + ALPHABET[(lo + hi) >> 1];
    if (hi - lo === 1) return out + ALPHABET[lo] + afterTail(prev, i + 1);
    out += ALPHABET[lo];
  }
}

/** compareRank orders by rank, ranked before unranked; ties go to `tie`. */
export function compareRank(a: { rank?: string }, b: { rank?: string }, tie: () => number): number {
  const ra = a.rank ?? '';
  const rb = b.rank ?? '';
  if (ra && rb && ra !== rb) return ra < rb ? -1 : 1;
  if (ra && !rb) return -1;
  if (!ra && rb) return 1;
  return tie();
}

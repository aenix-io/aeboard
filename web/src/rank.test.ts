import { describe, expect, it } from 'vitest';

import { compareRank, rankBetween } from './rank';

describe('rankBetween', () => {
  it('opens a list in the middle of the alphabet', () => {
    expect(rankBetween('', '')).toBe('i');
  });
  it('lands strictly between its neighbours', () => {
    for (const [a, b] of [['a', 'c'], ['a', 'b'], ['a', 'a1'], ['az', 'b'], ['', 'a'], ['zz', '']]) {
      const k = rankBetween(a, b);
      expect(k > a).toBe(true);
      if (b) expect(k < b).toBe(true);
      expect(k.endsWith('0')).toBe(false);
    }
  });
  it('keeps finding room when inserting at the same spot again and again', () => {
    let lo = 'a';
    const hi = 'b';
    for (let i = 0; i < 50; i++) {
      const k = rankBetween(lo, hi);
      expect(k > lo && k < hi).toBe(true);
      lo = k;
    }
  });
  it('refuses neighbours out of order', () => {
    expect(() => rankBetween('b', 'a')).toThrow();
    expect(() => rankBetween('a', 'a')).toThrow();
  });
});

describe('compareRank', () => {
  it('puts ranked items first, in rank order, then the rest by the tie-break', () => {
    const items = [{ id: 'x' }, { id: 'b', rank: 'm' }, { id: 'a', rank: 'c' }, { id: 'w' }];
    items.sort((p, q) => compareRank(p, q, () => p.id.localeCompare(q.id)));
    expect(items.map(i => i.id)).toEqual(['a', 'b', 'w', 'x']);
  });
});

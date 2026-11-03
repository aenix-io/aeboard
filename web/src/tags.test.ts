import { describe, expect, it } from 'vitest';

import { clickTag, filterBoards, isSelected, parseTags } from './tags';

describe('clickTag', () => {
  it('plain click selects only the clicked tag', () => {
    expect(clickTag(null, 'ops', false)).toEqual(['ops']);
    expect(clickTag(['ops', 'design'], 'q4', false)).toEqual(['q4']);
  });
  it('plain click on the only selected tag clears the filter', () => {
    expect(clickTag(['ops'], 'ops', false)).toBeNull();
    expect(clickTag(['ops'], 'OPS', false)).toBeNull();
  });
  it('plain click on one of several selected tags narrows to it', () => {
    expect(clickTag(['ops', 'q4'], 'ops', false)).toEqual(['ops']);
  });
  it('shift-click adds and removes', () => {
    expect(clickTag(null, 'ops', true)).toEqual(['ops']);
    expect(clickTag(['ops'], 'q4', true)).toEqual(['ops', 'q4']);
    expect(clickTag(['ops', 'q4'], 'ops', true)).toEqual(['q4']);
  });
  it('shift-click removing the last tag clears the filter (null, not [])', () => {
    expect(clickTag(['ops'], 'ops', true)).toBeNull();
  });
});

describe('filterBoards', () => {
  const boards = [
    { title: 'Roadmap', tags: ['q4', 'Product'] },
    { title: 'Retro', tags: ['team'] },
    { title: 'Scratch', tags: [] },
  ];
  it('no selection keeps every board, untagged included', () => {
    expect(filterBoards(boards, null)).toHaveLength(3);
  });
  it('several tags keep boards with ANY of them', () => {
    expect(filterBoards(boards, ['team', 'product']).map(b => b.title)).toEqual(['Roadmap', 'Retro']);
  });
  it('combines with a case-insensitive title search', () => {
    expect(filterBoards(boards, null, 're').map(b => b.title)).toEqual(['Retro']);
    expect(filterBoards(boards, ['q4'], 'retro')).toEqual([]);
  });
});

describe('isSelected / parseTags', () => {
  it('isSelected is false for no selection and case-insensitive otherwise', () => {
    expect(isSelected(null, 'x')).toBe(false);
    expect(isSelected(['Ops'], 'ops')).toBe(true);
  });
  it('parseTags trims, drops empties and case-insensitive duplicates', () => {
    expect(parseTags(' q4, Team ,,team, ')).toEqual(['q4', 'Team']);
  });
});

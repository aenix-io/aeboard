import { describe, expect, it } from 'vitest';

import { canMoveFolder, childrenOf, countIn, entriesOf, filterKind, inFolder, pathOf, placeAt, search, topLevel } from './library';

// work/
//   meetings/
//     q4/
// personal/
const folders = [
  { id: 'work', name: 'Work', parent: '' },
  { id: 'meet', name: 'Meetings', parent: 'work' },
  { id: 'q4', name: 'Q4', parent: 'meet' },
  { id: 'pers', name: 'Personal', parent: '' },
];
const docs = [
  { id: 'a', title: 'Roadmap', mode: 'edgeless' as const, folder: 'work' },
  { id: 'b', title: 'Standup notes', mode: 'page' as const, folder: 'meet' },
  { id: 'c', title: 'Q4 review', mode: 'page' as const, folder: 'q4' },
  { id: 'd', title: 'Ideas', mode: 'page' as const, folder: '' },
  { id: 'e', title: 'Orphan', mode: 'page' as const, folder: 'deleted-elsewhere' },
];

describe('pathOf', () => {
  it('walks from the top level down to the folder', () => {
    expect(pathOf(folders, 'q4').map(f => f.name)).toEqual(['Work', 'Meetings', 'Q4']);
  });
  it('is empty for the top level and for a folder that is not there', () => {
    expect(pathOf(folders, '')).toEqual([]);
    expect(pathOf(folders, 'nope')).toEqual([]);
  });
  it('stops on a cycle instead of looping', () => {
    const loop = [
      { id: 'x', name: 'X', parent: 'y' },
      { id: 'y', name: 'Y', parent: 'x' },
    ];
    expect(pathOf(loop, 'x').length).toBe(2);
  });
});

describe('inFolder', () => {
  it('lists direct subfolders by name and the documents right there', () => {
    const top = inFolder(folders, docs, '');
    expect(top.folders.map(f => f.name)).toEqual(['Personal', 'Work']);
    expect(top.docs.map(d => d.id)).toEqual(['d', 'e']);
    expect(inFolder(folders, docs, 'work').docs.map(d => d.id)).toEqual(['a']);
  });
  it('shows a document whose folder is gone at the top level', () => {
    expect(inFolder(folders, docs, '').docs.some(d => d.id === 'e')).toBe(true);
  });
});

describe('canMoveFolder', () => {
  it('refuses itself, its own subfolders and where it already is', () => {
    expect(canMoveFolder(folders, 'work', 'work')).toBe(false);
    expect(canMoveFolder(folders, 'work', 'q4')).toBe(false);
    expect(canMoveFolder(folders, 'meet', 'work')).toBe(false);
  });
  it('allows a sibling or the top level', () => {
    expect(canMoveFolder(folders, 'meet', 'pers')).toBe(true);
    expect(canMoveFolder(folders, 'q4', '')).toBe(true);
  });
});

describe('countIn / search / filterKind', () => {
  it('counts documents in a folder and below it', () => {
    expect(countIn(folders, docs, 'work')).toBe(3);
    expect(countIn(folders, docs, 'pers')).toBe(0);
  });
  it('searches every folder, case-insensitively; nothing for an empty query', () => {
    expect(search(docs, 'q4').map(d => d.id)).toEqual(['c']);
    expect(search(docs, '  ')).toEqual([]);
  });
  it('keeps one kind or all', () => {
    expect(filterKind(docs, 'edgeless').map(d => d.id)).toEqual(['a']);
    expect(filterKind(docs, 'all').length).toBe(5);
  });
});

describe('ordering', () => {
  const fs = [
    { id: 'f1', name: 'Zeta', parent: '' },
    { id: 'f2', name: 'Alpha', parent: '' },
  ];
  const ds = [
    { id: 'd1', title: 'Beta', mode: 'page' as const, folder: '' },
    { id: 'd2', title: 'Gamma', mode: 'page' as const, folder: '' },
  ];
  it('without ranks, a folder lists its contents by name, folders and documents together', () => {
    expect(entriesOf(fs, ds, '').map(e => e.name)).toEqual(['Alpha', 'Beta', 'Gamma', 'Zeta']);
  });
  it('the first drop into an unranked folder ranks it all, keeping what people saw', () => {
    // Drag Zeta (f1) to the top.
    const ps = placeAt(fs, ds, '', { kind: 'folder', id: 'f1' }, 0);
    const ranked = <T extends { id: string; rank?: string }>(list: T[]): T[] =>
      list.map(x => ({ ...x, rank: ps.find(p => p.id === x.id)?.rank ?? x.rank }));
    expect(entriesOf(ranked(fs), ranked(ds), '').map(e => e.name)).toEqual(['Zeta', 'Alpha', 'Beta', 'Gamma']);
  });
  it('in a ranked folder, a drop writes one key, between the new neighbours', () => {
    const rf = [
      { id: 'f1', name: 'Zeta', parent: '', rank: 'c' },
      { id: 'f2', name: 'Alpha', parent: '', rank: 'k' },
    ];
    const rd = [{ id: 'd1', title: 'Beta', mode: 'page' as const, folder: '', rank: 'r' }];
    const ps = placeAt(rf, rd, '', { kind: 'doc', id: 'd1' }, 1); // between Zeta and Alpha
    expect(ps.length).toBe(1);
    expect(ps[0].rank > 'c' && ps[0].rank < 'k').toBe(true);
  });
  it('a document moved into another folder takes a key there', () => {
    const ps = placeAt(fs, ds, 'f2', { kind: 'doc', id: 'd1' }, 0);
    expect(ps).toEqual([{ kind: 'doc', id: 'd1', rank: 'i' }]);
  });
});

describe('documents inside documents', () => {
  const doc = (id: string, links: string[] = [], folder = '') => ({ id, title: id, mode: 'page' as const, folder, links });
  const folders = [{ id: 'f', name: 'F', parent: '' }];

  it('lists the linked documents that exist, in link order', () => {
    const docs = [doc('a', ['c', 'gone', 'a', 'b']), doc('b'), doc('c')];
    expect(childrenOf(docs, docs[0]).map(d => d.id)).toEqual(['c', 'b']);
  });

  it('shows a linked document inside its parent, not at the top', () => {
    const docs = [doc('a', ['b']), doc('b', ['c']), doc('c'), doc('d')];
    expect(topLevel(folders, docs).map(d => d.id)).toEqual(['a', 'd']);
  });

  it('keeps a document in a folder there even when it is linked', () => {
    const docs = [doc('a', ['b']), doc('b', [], 'f')];
    expect(topLevel(folders, docs).map(d => d.id)).toEqual(['a', 'b']);
  });

  it('nests a top-level document linked from one in a folder', () => {
    const docs = [doc('a', ['b'], 'f'), doc('b')];
    expect(topLevel(folders, docs).map(d => d.id)).toEqual(['a']);
  });

  it('keeps one member of a closed cycle at the top', () => {
    const docs = [doc('a', ['b']), doc('b', ['a'])];
    expect(topLevel(folders, docs).map(d => d.id)).toEqual(['a']);
  });
});

import { describe, expect, it } from 'vitest';

import { type RowCtx, resolveDrop } from './tree';

// P            (top level, last)
//   A          (inside P)
//   B          (inside P, last)
//     C        (inside B, last)
const top = { kind: 'folder' as const, id: '' };
const P: RowCtx = { kind: 'doc', id: 'P', node: 'P', container: top, depth: 0, last: true, open: true, up: [] };
const A: RowCtx = { kind: 'doc', id: 'A', node: 'P/A', container: { kind: 'doc', id: 'P' }, depth: 1, last: false, open: false, up: [P] };
const B: RowCtx = { kind: 'doc', id: 'B', node: 'P/B', container: { kind: 'doc', id: 'P' }, depth: 1, last: true, open: true, up: [P] };
const C: RowCtx = { kind: 'doc', id: 'C', node: 'P/B/C', container: { kind: 'doc', id: 'B' }, depth: 2, last: true, open: false, up: [P, B] };

describe('resolveDrop', () => {
  it('puts an item before or after a nested row, among its siblings', () => {
    expect(resolveDrop(A, 'before', 1)).toEqual({ container: { kind: 'doc', id: 'P' }, before: 'A', level: 1 });
    expect(resolveDrop(A, 'after', 1)).toEqual({ container: { kind: 'doc', id: 'P' }, after: 'A', level: 1 });
  });

  it('puts an item first inside an open row, from its bottom edge', () => {
    expect(resolveDrop(P, 'after', 0)).toEqual({ container: { kind: 'doc', id: 'P' }, first: true, level: 1 });
  });

  it('puts an item into a row from its middle', () => {
    expect(resolveDrop(A, 'into', 1)).toEqual({ container: { kind: 'doc', id: 'A' }, level: 2 });
  });

  it('lets the pointer take an item out of the last row to a level above', () => {
    expect(resolveDrop(C, 'after', 2)).toEqual({ container: { kind: 'doc', id: 'B' }, after: 'C', level: 2 });
    expect(resolveDrop(C, 'after', 1)).toEqual({ container: { kind: 'doc', id: 'P' }, after: 'B', level: 1 });
    expect(resolveDrop(C, 'after', 0)).toEqual({ container: top, after: 'P', level: 0 });
  });

  it('does not go out past a row that is not its level last', () => {
    const D: RowCtx = { ...C, up: [P, { ...B, last: false }] };
    expect(resolveDrop({ ...D, last: false }, 'after', 0)).toEqual({ container: { kind: 'doc', id: 'B' }, after: 'C', level: 2 });
    expect(resolveDrop(D, 'after', 0)).toEqual({ container: { kind: 'doc', id: 'P' }, after: 'B', level: 1 });
  });
});

// Where a drop in the sidebar's tree lands, kept apart from the component
// so it is tested on its own.

/** A level of the tree: a folder's contents ("" is the top level) or a
 *  document's children (the documents it links to). */
export interface Container {
  kind: 'folder' | 'doc';
  id: string;
}

/** A row of the tree and where it stands. */
export interface RowCtx {
  kind: 'folder' | 'doc';
  id: string;
  /** The row's own key: its path in the tree. */
  node: string;
  /** The level the row is in. */
  container: Container;
  depth: number;
  /** Whether the row is its level's last. */
  last: boolean;
  /** Whether the row is expanded and shows entries under it. */
  open: boolean;
  /** The rows it is under, the outermost first. */
  up: RowCtx[];
}

/** The part of a row a drag is over: its top, its bottom or its middle. */
export type Zone = 'before' | 'after' | 'into';

/** Drop is where a dropped item goes: into a level, before or after one of
 *  its entries, first, or (none of them) at its end. level is the depth the
 *  item lands at, for the line showing it. */
export interface Drop {
  container: Container;
  before?: string;
  after?: string;
  first?: boolean;
  level: number;
}

/** resolveDrop turns a zone of a row into a drop. Below an open row the item
 *  goes in as its first entry. Below a level's last row the pointer's level
 *  picks how far out it goes: after the row itself, or after one of the rows
 *  it is under — as long as each row in between is its level's last too, so
 *  the line drawn is where the item will show. */
export function resolveDrop(row: RowCtx, zone: Zone, level: number): Drop {
  const self: Container = { kind: row.kind, id: row.id };
  if (zone === 'into') return { container: self, level: row.depth + 1 };
  if (zone === 'before') return { container: row.container, before: row.id, level: row.depth };
  if (row.open) return { container: self, first: true, level: row.depth + 1 };
  let target = row;
  for (let i = row.up.length - 1; i >= 0 && target.last && level < target.depth; i--) target = row.up[i];
  return { container: target.container, after: target.id, level: target.depth };
}

/** levelAt is the depth a pointer at x stands for, given how far each
 *  level is indented. */
export function levelAt(x: number, left: number, indent: (depth: number) => number, max: number): number {
  let level = 0;
  while (level < max && x - left >= indent(level + 1)) level++;
  return level;
}

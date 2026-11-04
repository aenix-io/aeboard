// The start page's folder logic — paths, contents, moves, search — kept out
// of the components so it is tested on its own.

import { compareRank, rankBetween } from './rank';

export interface FolderNode {
  id: string;
  name: string;
  parent: string;
  rank?: string;
}

export interface DocNode {
  id: string;
  title: string;
  mode: 'edgeless' | 'page';
  folder: string;
  rank?: string;
}

/** An entry of a folder, folder or document, in the folder's order. */
export type Entry<D extends DocNode = DocNode> =
  | { kind: 'folder'; id: string; rank?: string; name: string; folder: FolderNode }
  | { kind: 'doc'; id: string; rank?: string; name: string; doc: D };

const byRankThenName = (a: { rank?: string; name: string; id: string }, b: { rank?: string; name: string; id: string }) =>
  compareRank(a, b, () => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));

/** entriesOf is a folder's contents in its order — the order people set by
 *  dragging (rank), then the rest by name — folders and documents together. */
export function entriesOf<D extends DocNode>(folders: FolderNode[], docs: D[], id: string): Entry<D>[] {
  const here = inFolder(folders, docs, id);
  const all: Entry<D>[] = [
    ...here.folders.map(f => ({ kind: 'folder' as const, id: f.id, rank: f.rank, name: f.name, folder: f })),
    ...here.docs.map(d => ({ kind: 'doc' as const, id: d.id, rank: d.rank, name: d.title || '\uffff', doc: d })),
  ];
  return all.sort(byRankThenName);
}

/** pathOf is the chain of folders from the top level down to id (inclusive);
 *  [] for the top level or a folder that is not there. */
export function pathOf(folders: FolderNode[], id: string): FolderNode[] {
  const byID = new Map(folders.map(f => [f.id, f]));
  const out: FolderNode[] = [];
  const seen = new Set<string>();
  for (let at = byID.get(id); at && !seen.has(at.id); at = byID.get(at.parent)) {
    seen.add(at.id);
    out.unshift(at);
  }
  return out;
}

/** inFolder lists a folder's direct subfolders and documents ("" is the top
 *  level). A document whose folder is gone shows at the top level. */
export function inFolder<D extends DocNode>(
  folders: FolderNode[],
  docs: D[],
  id: string,
): { folders: FolderNode[]; docs: D[] } {
  const known = new Set(folders.map(f => f.id));
  return {
    folders: folders
      .filter(f => f.parent === id)
      .sort((a, b) => byRankThenName(a, b)),
    docs: docs
      .filter(d => (known.has(d.folder) ? d.folder : '') === id)
      .sort((a, b) => byRankThenName({ ...a, name: a.title || '\uffff' }, { ...b, name: b.title || '\uffff' })),
  };
}

/** canMoveFolder reports whether folder id may go into target: not into
 *  itself, not into one of its own subfolders, not where it already is. */
export function canMoveFolder(folders: FolderNode[], id: string, target: string): boolean {
  if (id === target) return false;
  const f = folders.find(x => x.id === id);
  if (!f || f.parent === target) return false;
  return !pathOf(folders, target).some(p => p.id === id);
}

/** countIn is how many documents a folder holds, its subfolders included. */
export function countIn(folders: FolderNode[], docs: DocNode[], id: string): number {
  const below = new Set([id]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const f of folders) {
      if (below.has(f.parent) && !below.has(f.id)) {
        below.add(f.id);
        grew = true;
      }
    }
  }
  return docs.filter(d => below.has(d.folder)).length;
}

/** search finds documents in every folder whose title contains the query
 *  (case-insensitive); an empty query finds nothing. */
export function search<D extends DocNode>(docs: D[], query: string): D[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  return docs.filter(d => d.title.toLowerCase().includes(q));
}

/** The list's kind switch: every document, only pages, or only boards. */
export type KindFilter = 'all' | 'edgeless' | 'page';

/** filterKind keeps the documents of one kind (all of them for "all"). */
export function filterKind<T extends { mode: 'edgeless' | 'page' }>(docs: T[], kind: KindFilter): T[] {
  return kind === 'all' ? docs : docs.filter(d => d.mode === kind);
}

/** A rank to write: an item and its new key. */
export interface Placement {
  kind: 'folder' | 'doc';
  id: string;
  rank: string;
}

/** placeAt computes where item lands when dropped at `index` of folder
 *  `parent`'s entries (counted without the item itself). Usually that is one
 *  new key for the item, between its new neighbours. When the folder still
 *  has unranked entries (nobody has ordered it yet), the whole folder is
 *  ranked in its current order first, so the drop keeps what people see. */
export function placeAt(
  folders: FolderNode[],
  docs: DocNode[],
  parent: string,
  item: { kind: 'folder' | 'doc'; id: string },
  index: number,
): Placement[] {
  const siblings = entriesOf(folders, docs, parent).filter(e => !(e.kind === item.kind && e.id === item.id));
  const at = Math.max(0, Math.min(index, siblings.length));
  if (siblings.every(e => e.rank)) {
    const prev = siblings[at - 1]?.rank ?? '';
    const next = siblings[at]?.rank ?? '';
    return [{ ...item, rank: rankBetween(prev, next) }];
  }
  const order: Array<{ kind: 'folder' | 'doc'; id: string; rank?: string }> = [
    ...siblings.slice(0, at),
    item,
    ...siblings.slice(at),
  ];
  const out: Placement[] = [];
  let prev = '';
  for (const e of order) {
    prev = rankBetween(prev, '');
    if (e === item || e.rank !== prev) out.push({ kind: e.kind, id: e.id, rank: prev });
  }
  return out;
}

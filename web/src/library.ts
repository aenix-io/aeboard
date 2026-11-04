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
  /** The document this one lives inside; its folder is "" then. */
  parent?: string;
  /** The documents this one links to: shortcuts inside it, for those that
   *  live elsewhere. */
  links?: string[];
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
  return placeAmong(entriesOf(folders, docs, parent), item, index);
}

/** placeAmong is placeAt over any ordered list of siblings. */
export function placeAmong(
  entries: Array<{ kind: 'folder' | 'doc'; id: string; rank?: string }>,
  item: { kind: 'folder' | 'doc'; id: string },
  index: number,
): Placement[] {
  const siblings = entries.filter(e => !(e.kind === item.kind && e.id === item.id));
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

/** atFolderLevel are the documents that live in a folder or at the top
 *  level: not inside another document (one whose parent is gone counts as
 *  at the top). */
export function atFolderLevel<D extends DocNode>(docs: D[]): D[] {
  const ids = new Set(docs.map(d => d.id));
  const loose = cycleBreaks(docs);
  return docs.filter(d => !d.parent || !ids.has(d.parent) || loose.has(d.id));
}

/** cycleBreaks are the documents that stand at the top to break a loop of
 *  parents: two replicas, each moving one document inside the other, merge
 *  into X inside Y inside X, which would hide both. One member of each loop
 *  (the least id, so every client picks the same) is shown at the top. */
export function cycleBreaks(docs: DocNode[]): Set<string> {
  const byID = new Map(docs.map(d => [d.id, d]));
  const out = new Set<string>();
  const done = new Set<string>();
  for (const start of docs) {
    const path: string[] = [];
    const onPath = new Set<string>();
    let at: DocNode | undefined = start;
    while (at && !done.has(at.id) && !onPath.has(at.id)) {
      path.push(at.id);
      onPath.add(at.id);
      at = at.parent ? byID.get(at.parent) : undefined;
    }
    if (at && onPath.has(at.id)) {
      const loop = path.slice(path.indexOf(at.id));
      out.add(loop.reduce((a, b) => (a < b ? a : b)));
    }
    path.forEach(id => done.add(id));
  }
  return out;
}

/** inside is what shows in document d: the documents that live in it, in
 *  their order, then shortcuts — the documents it links to that live
 *  elsewhere, in link order. */
export function inside<D extends DocNode>(docs: D[], d: DocNode): { docs: D[]; shortcuts: D[] } {
  const loose = cycleBreaks(docs);
  const own = docs
    .filter(x => x.parent === d.id && x.id !== d.id && !loose.has(x.id))
    .sort((a, b) => byRankThenName({ ...a, name: a.title || '\uffff' }, { ...b, name: b.title || '\uffff' }));
  const byID = new Map(docs.map(x => [x.id, x]));
  const seen = new Set<string>();
  const shortcuts = (d.links ?? []).flatMap(id => {
    const x = byID.get(id);
    if (!x || x.id === d.id || x.parent === d.id || seen.has(id)) return [];
    seen.add(id);
    return [x];
  });
  return { docs: own, shortcuts };
}

/** A step of a path to a document: a folder or a document above it. */
export interface Crumb {
  kind: 'folder' | 'doc';
  id: string;
  name: string;
}

/** homePath is the way down to where document id lives: its folders, then
 *  the documents it is inside, outermost first (not the document itself). */
export function homePath(folders: FolderNode[], docs: DocNode[], id: string): Crumb[] {
  const byID = new Map(docs.map(d => [d.id, d]));
  const loose = cycleBreaks(docs);
  const up: Crumb[] = [];
  const seen = new Set<string>([id]);
  let d = byID.get(id);
  while (d?.parent && byID.has(d.parent) && !seen.has(d.parent) && !loose.has(d.id)) {
    const p = byID.get(d.parent)!;
    seen.add(p.id);
    up.unshift({ kind: 'doc', id: p.id, name: p.title });
    d = p;
  }
  const folder = d?.folder ?? '';
  return [...pathOf(folders, folder).map(f => ({ kind: 'folder' as const, id: f.id, name: f.name })), ...up];
}

/** isInside reports whether document id lives somewhere under doc
 *  `ancestor` (so ancestor may not go inside it). */
export function isInside(docs: DocNode[], id: string, ancestor: string): boolean {
  const byID = new Map(docs.map(d => [d.id, d]));
  const seen = new Set<string>();
  for (let at = byID.get(id)?.parent; at && !seen.has(at); at = byID.get(at)?.parent) {
    if (at === ancestor) return true;
    seen.add(at);
  }
  return false;
}

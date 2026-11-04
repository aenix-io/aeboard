// The start page's folder logic — paths, contents, moves, search — kept out
// of the components so it is tested on its own.

export interface FolderNode {
  id: string;
  name: string;
  parent: string;
}

export interface DocNode {
  id: string;
  title: string;
  mode: 'edgeless' | 'page';
  folder: string;
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
    folders: folders.filter(f => f.parent === id).sort((a, b) => a.name.localeCompare(b.name)),
    docs: docs.filter(d => (known.has(d.folder) ? d.folder : '') === id),
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

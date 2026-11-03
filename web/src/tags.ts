// Tag chip selection and board filtering — the pure rules behind the board
// list, kept out of the components so they are tested on their own.

/** The selected tags, or null when no chip is selected (show every board). */
export type TagSelection = string[] | null;

const key = (t: string) => t.toLowerCase();

/**
 * clickTag is the chip-click rule, the same as aeman's team chips:
 * a plain click selects just this tag, and clicking the only selected tag
 * clears the filter; Shift-click adds the tag to the selection or removes it.
 */
export function clickTag(sel: TagSelection, tag: string, shift: boolean): TagSelection {
  const has = (s: string[]) => s.some(t => key(t) === key(tag));
  if (shift) {
    const base = sel ?? [];
    const next = has(base) ? base.filter(t => key(t) !== key(tag)) : [...base, tag];
    return next.length ? next : null;
  }
  return sel?.length === 1 && key(sel[0]) === key(tag) ? null : [tag];
}

export function isSelected(sel: TagSelection, tag: string): boolean {
  return sel?.some(t => key(t) === key(tag)) ?? false;
}

export interface Taggable {
  title: string;
  tags: string[];
}

/**
 * filterBoards keeps the boards carrying ANY of the selected tags (null keeps
 * all) whose title contains the query. Tags compare case-insensitively.
 */
export function filterBoards<T extends Taggable>(boards: T[], sel: TagSelection, query = ''): T[] {
  const want = sel?.map(key);
  const q = query.trim().toLowerCase();
  return boards.filter(
    b =>
      (!want || b.tags.some(t => want.includes(key(t)))) &&
      (!q || b.title.toLowerCase().includes(q)),
  );
}

/** parseTags splits a comma-separated input into trimmed, de-duplicated tags. */
export function parseTags(input: string): string[] {
  const out: string[] = [];
  for (const raw of input.split(',')) {
    const t = raw.trim();
    if (t && !out.some(o => key(o) === key(t))) out.push(t);
  }
  return out;
}

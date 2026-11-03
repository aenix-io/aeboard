import { describe, expect, it } from 'vitest';

import { syncView, UNPUSHED_WARN_MS } from './sync';

const now = Date.parse('2026-11-03T12:00:00Z');
const ago = (ms: number) => new Date(now - ms).toISOString();

describe('syncView', () => {
  it('hides the badge when everything is written and pushed', () => {
    expect(syncView({ backend: 'git', pending: 0, unpushed: 0 }, now)).toEqual({ count: 0, title: '', warning: null });
    expect(syncView(null, now).count).toBe(0);
  });
  it('counts uncommitted boards and unpushed commits together', () => {
    const v = syncView({ backend: 'git', pending: 2, unpushed: 1 }, now);
    expect(v.count).toBe(3);
    expect(v.title).toBe('2 boards with changes not yet committed, 1 commit not yet pushed');
  });
  it('warns once unpushed changes are older than the threshold, not before', () => {
    expect(syncView({ backend: 'git', pending: 0, unpushed: 1, unpushedSince: ago(UNPUSHED_WARN_MS - 1000) }, now).warning).toBeNull();
    expect(syncView({ backend: 'git', pending: 0, unpushed: 1, unpushedSince: ago(7 * 60000) }, now).warning).toBe(
      'Changes are saved on the server but have not been pushed for 7 minutes',
    );
  });
  it('shows the last sync error', () => {
    expect(syncView({ backend: 'git', pending: 0, unpushed: 1, error: 'push rejected' }, now).warning).toBe(
      'Sync problem: push rejected',
    );
  });
});

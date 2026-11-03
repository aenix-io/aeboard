import { afterEach, describe, expect, it, vi } from 'vitest';

import { api } from './api';

const answer = (status: number, body: unknown) =>
  vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));

describe('api.me', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('is the signed-in visitor with their access', async () => {
    vi.stubGlobal('fetch', answer(200, { login: 'alice', access: 'read', hosted: true }));
    expect(await api.me()).toEqual({ kind: 'known', me: { login: 'alice', access: 'read', hosted: true } });
  });

  it('tells a hosted sign-in apart from an anonymous local server', async () => {
    vi.stubGlobal('fetch', answer(401, { error: 'not signed in', hosted: true, forge: 'GitHub' }));
    expect(await api.me()).toEqual({ kind: 'signedOut', forge: 'GitHub' });
    vi.stubGlobal('fetch', answer(401, { error: 'anonymous' }));
    expect(await api.me()).toEqual({ kind: 'anonymous' });
  });

  it('reports any other failure', async () => {
    vi.stubGlobal('fetch', answer(403, { error: 'access could not be decided' }));
    await expect(api.me()).rejects.toThrow('access could not be decided');
  });
});

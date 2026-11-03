// Board attachments (images, stickers, files) live on the server, addressed
// by content hash, not in the Yjs document: the doc only carries the key.
import type { BlobSource } from '@blocksuite/affine/sync';

export class ServerBlobSource implements BlobSource {
  readonly name = 'aeboard-server';
  readonly readonly = false;
  // Keys are content hashes, so a cached blob can never go stale.
  private readonly cache = new Map<string, Blob>();

  constructor(private readonly boardId: string) {}

  private url(key: string) {
    return `/api/v1/boards/${encodeURIComponent(this.boardId)}/blobs/${encodeURIComponent(key)}`;
  }

  async get(key: string): Promise<Blob | null> {
    const hit = this.cache.get(key);
    if (hit) return hit;
    const resp = await fetch(this.url(key));
    if (resp.status === 404) return null;
    if (!resp.ok) throw new Error(`attachment ${key}: ${resp.status}`);
    const blob = await resp.blob();
    this.cache.set(key, blob);
    return blob;
  }

  // set is the main peer of BlockSuite's BlobEngine: inserting an image waits
  // for this upload, so a failure (offline, too large) surfaces instead of
  // leaving a picture only this browser can see.
  async set(key: string, value: Blob): Promise<string> {
    const resp = await fetch(this.url(key), {
      method: 'PUT',
      headers: value.type ? { 'Content-Type': value.type } : undefined,
      body: value,
    });
    if (!resp.ok) throw new Error(`upload ${key}: ${resp.status}`);
    this.cache.set(key, value);
    return key;
  }

  // Blobs are shared by content across boards and undo history; a client
  // never deletes one.
  async delete(): Promise<void> {}

  async list(): Promise<string[]> {
    return [...this.cache.keys()];
  }
}

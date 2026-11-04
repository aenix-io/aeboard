// The server's sync status (/api/v1/status), as the header badge reads it.
import { useEffect, useState } from 'react';

export interface SyncStatus {
  backend: 'dir' | 'git';
  remote?: string;
  /** Boards with changes not yet committed. */
  pending: number;
  /** Commits not yet pushed. */
  unpushed: number;
  lastSync?: string;
  unpushedSince?: string;
  error?: string;
  /** Grows with every change to the document list (see catalog.tsx). */
  catalog?: number;
}

/** How long unpushed changes may wait before the badge warns (as aeman's
 *  "degraded" health). */
export const UNPUSHED_WARN_MS = 5 * 60 * 1000;

export interface SyncView {
  /** The number on the badge; 0 hides it. */
  count: number;
  /** Tooltip. */
  title: string;
  /** A warning to show next to the badge, or null. */
  warning: string | null;
}

/** syncView turns a status into what the badge shows. */
export function syncView(st: SyncStatus | null, now = Date.now()): SyncView {
  if (!st) return { count: 0, title: '', warning: null };
  const count = st.pending + st.unpushed;
  const parts: string[] = [];
  if (st.pending) parts.push(`${st.pending} board${st.pending === 1 ? '' : 's'} with changes not yet committed`);
  if (st.unpushed) parts.push(`${st.unpushed} commit${st.unpushed === 1 ? '' : 's'} not yet pushed`);
  let warning: string | null = null;
  if (st.backend === 'git' && st.unpushedSince) {
    const age = now - Date.parse(st.unpushedSince);
    if (age >= UNPUSHED_WARN_MS) {
      const min = Math.max(1, Math.round(age / 60000));
      warning = `Changes are saved on the server but have not been pushed for ${min} minute${min === 1 ? '' : 's'}`;
    }
  }
  if (st.error) warning = warning ? `${warning}: ${st.error}` : `Sync problem: ${st.error}`;
  return { count, title: parts.join(', '), warning };
}

/** useSyncStatus follows /api/v1/status/events (EventSource reconnects on
 *  its own after a server restart). */
export function useSyncStatus(): SyncStatus | null {
  const [st, setSt] = useState<SyncStatus | null>(null);
  useEffect(() => {
    const es = new EventSource('/api/v1/status/events');
    es.onmessage = e => {
      try {
        setSt(JSON.parse(e.data) as SyncStatus);
      } catch {
        /* ignore a malformed frame */
      }
    };
    return () => es.close();
  }, []);
  return st;
}

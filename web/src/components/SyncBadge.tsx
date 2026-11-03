import { syncView, type SyncStatus } from '../sync';

/** SyncBadge is aeman's sync pill: a spinning arrow and how many changes
 *  have not reached git yet; hidden when there are none. */
export function SyncBadge({ status }: { status: SyncStatus | null }) {
  const v = syncView(status);
  return (
    <>
      {v.warning && (
        <span className="sync-warning" role="status">
          {v.warning}
        </span>
      )}
      {v.count > 0 && (
        <span className="sync-badge" title={v.title} aria-label={v.title}>
          <svg
            width="11"
            height="11"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M21 2v6h-6" />
            <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
            <path d="M3 22v-6h6" />
            <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
          </svg>
          {v.count}
        </span>
      )}
    </>
  );
}

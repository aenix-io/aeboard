import { type ReactNode, useState } from 'react';

import type { Board } from '../api';
import { useCatalog } from '../catalog';
import { navigate } from '../router';
import { untitled } from './BoardPage';
import { Confirm } from './Confirm';
import { BoardIcon, PageIcon } from './icons';

/** TrashList is the trash, as AFFiNE's: documents moved there, each to
 *  restore or delete for good. */
export function TrashList({ lead, whoami, status }: { lead?: ReactNode; whoami: ReactNode; status?: ReactNode }) {
  const cat = useCatalog();
  const [deleting, setDeleting] = useState<Board | null>(null);
  return (
    <div className="page">
      <header className="list-head">
        {lead}
        <h1 className="list-title">Trash</h1>
        <div className="list-tools">
          {status}
          {whoami}
        </div>
      </header>
      {cat.error && <p className="error">{cat.error}</p>}
      {cat.docs !== null && cat.trash.length === 0 && <p className="muted">Deleted documents will appear here.</p>}
      <ul className="trash-list">
        {cat.trash.map(b => (
          <li key={b.id} className="trash-row">
            <a
              className="trash-title"
              href={`/b/${b.id}`}
              onClick={e => {
                if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
                e.preventDefault();
                navigate(`/b/${b.id}`);
              }}
            >
              <span className={`kind kind-${b.mode}`}>{b.mode === 'page' ? <PageIcon /> : <BoardIcon />}</span>
              {b.title || untitled(b.mode)}
            </a>
            <span className="board-meta">Deleted {new Date(b.trashed ?? '').toLocaleString()}</span>
            {cat.canWrite && (
              <span className="trash-actions">
                <button type="button" onClick={() => void cat.restoreDoc(b.id).catch(() => {})}>
                  Restore
                </button>
                <button type="button" className="danger" onClick={() => setDeleting(b)}>
                  Delete permanently
                </button>
              </span>
            )}
          </li>
        ))}
      </ul>
      {deleting && (
        <Confirm
          title="Delete permanently?"
          body={`${deleting.title || untitled(deleting.mode)} will be deleted for good. This cannot be undone.`}
          action="Delete permanently"
          danger
          onCancel={() => setDeleting(null)}
          onConfirm={() => {
            const id = deleting.id;
            setDeleting(null);
            void cat.deleteDoc(id).catch(() => {});
          }}
        />
      )}
    </div>
  );
}

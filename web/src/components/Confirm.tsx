import { useEffect } from 'react';

/** Confirm asks before an action: a small dialog, never the browser's
 *  blocking confirm(). */
export function Confirm({
  title,
  body,
  action,
  danger,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: string;
  action: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onCancel();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onCancel]);
  return (
    <div className="dialog-scrim" onClick={onCancel}>
      <div className="dialog" role="alertdialog" aria-modal="true" aria-label={title} onClick={e => e.stopPropagation()}>
        <h2>{title}</h2>
        <p className="confirm-body">{body}</p>
        <div className="confirm-actions">
          <button type="button" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className={danger ? 'danger' : 'primary'} autoFocus onClick={onConfirm}>
            {action}
          </button>
        </div>
      </div>
    </div>
  );
}

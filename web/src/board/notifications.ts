// BlockSuite's notification service, done as small toasts in the corner.
// Without one, actions that only report what they did — "Display in page" on
// a note, for one — look like they did nothing.
import { NotificationExtension } from '@blocksuite/affine/shared/services';
import { render, type TemplateResult } from 'lit';

type Content = string | TemplateResult;

function stack(): HTMLElement {
  let el = document.getElementById('aenotes-toasts');
  if (!el) {
    el = document.createElement('div');
    el.id = 'aenotes-toasts';
    el.setAttribute('role', 'status');
    document.body.append(el);
  }
  return el;
}

function put(into: HTMLElement, c: Content | undefined) {
  if (c === undefined) return;
  if (typeof c === 'string') into.textContent = c;
  else render(c, into);
}

// aenotes has no outline sidebar: BlockSuite's pointers to it go.
function dropOutline(msg: Content | undefined): Content | undefined {
  return typeof msg === 'string' ? msg.replace(/\s*Find it in the TOC for quick navigation\.?$/, '') : msg;
}

function show(opts: {
  title: Content;
  message?: Content;
  accent?: string;
  duration?: number;
  abort?: AbortSignal;
  actions?: { key: string; label: Content; onClick: () => void }[];
  onClose?: () => void;
}) {
  const card = document.createElement('div');
  card.className = `aenotes-toast aenotes-toast-${opts.accent ?? 'info'}`;
  const title = document.createElement('div');
  title.className = 'aenotes-toast-title';
  put(title, opts.title);
  card.append(title);
  const message = dropOutline(opts.message);
  if (message) {
    const m = document.createElement('div');
    m.className = 'aenotes-toast-message';
    put(m, message);
    card.append(m);
  }
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    card.remove();
    opts.onClose?.();
  };
  const actions = (opts.actions ?? []).filter(a => a.key !== 'view-in-toc');
  if (actions.length) {
    const row = document.createElement('div');
    row.className = 'aenotes-toast-actions';
    for (const a of actions) {
      const b = document.createElement('button');
      b.type = 'button';
      put(b, a.label);
      b.onclick = () => {
        a.onClick();
        close();
      };
      row.append(b);
    }
    card.append(row);
  }
  stack().append(card);
  opts.abort?.addEventListener('abort', close, { once: true });
  const duration = opts.duration ?? 4000;
  if (duration > 0) setTimeout(close, duration);
}

export const ToastNotifications = NotificationExtension({
  toast: (message, options) => show({ title: message, duration: options?.duration }),
  notify: opts => show(opts),
  // Rare in an edgeless editor (deleting a frame's contents, renaming): the
  // browser's own dialogs are enough.
  confirm: async ({ title, message }) =>
    window.confirm([title, message].filter(t => typeof t === 'string').join('\n\n')),
  prompt: async ({ title, message, autofill }) =>
    window.prompt([title, message].filter(t => typeof t === 'string').join('\n\n'), autofill ?? ''),
});

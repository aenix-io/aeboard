import { type ReactNode, useEffect, useRef, useState } from 'react';

/** RowMenu is a row's small hover button with a dropdown. The list is fixed
 *  to the window (the tree scrolls and would clip it) and closes on a click
 *  elsewhere, Escape or a choice — not when the pointer wanders off, which
 *  would close it on the way from the button to the list. */
export function RowMenu({ label, glyph, items }: { label: string; glyph: ReactNode; items: Array<[string, () => void]> }) {
  const [at, setAt] = useState<{ top: number; left: number } | null>(null);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!at) return;
    const close = (e: Event) => {
      if (e instanceof KeyboardEvent && e.key !== 'Escape') return;
      if (e instanceof PointerEvent && root.current?.contains(e.target as Node)) return;
      setAt(null);
    };
    document.addEventListener('pointerdown', close, true);
    document.addEventListener('keydown', close);
    window.addEventListener('resize', close);
    return () => {
      document.removeEventListener('pointerdown', close, true);
      document.removeEventListener('keydown', close);
      window.removeEventListener('resize', close);
    };
  }, [at]);
  return (
    <span className="sb-menu" ref={root}>
      <button
        type="button"
        className="sb-icon-button"
        aria-label={label}
        title={label}
        aria-expanded={at !== null}
        onClick={e => {
          e.preventDefault();
          e.stopPropagation();
          if (at) {
            setAt(null);
            return;
          }
          const r = e.currentTarget.getBoundingClientRect();
          setAt({ top: r.bottom + 4, left: Math.min(r.left, window.innerWidth - 180) });
        }}
      >
        {glyph}
      </button>
      {at && (
        <span className="item-menu-list sb-menu-list" role="menu" style={{ position: 'fixed', top: at.top, left: at.left }}>
          {items.map(([text, run]) => (
            <button
              key={text}
              type="button"
              role="menuitem"
              onClick={e => {
                e.stopPropagation();
                setAt(null);
                run();
              }}
            >
              {text}
            </button>
          ))}
        </span>
      )}
    </span>
  );
}

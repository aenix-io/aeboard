import { clickTag, isSelected, type TagSelection } from '../tags';

interface Props {
  tags: string[];
  selected: TagSelection;
  onSelect: (sel: TagSelection) => void;
}

/** TagChips is the filter row: click selects one tag, Shift-click adds/removes. */
export function TagChips({ tags, selected, onSelect }: Props) {
  if (tags.length === 0) return null;
  return (
    <div className="chips" role="group" aria-label="Filter by tag">
      {tags.map(t => {
        const on = isSelected(selected, t);
        return (
          <button
            key={t}
            type="button"
            className={`chip chip-filter${on ? ' chip-on' : ''}`}
            aria-pressed={on}
            title="Click to select · Shift-click to add"
            onClick={e => onSelect(clickTag(selected, t, e.shiftKey))}
          >
            {t}
          </button>
        );
      })}
      {selected && (
        <button type="button" className="link-button" onClick={() => onSelect(null)}>
          clear
        </button>
      )}
    </div>
  );
}

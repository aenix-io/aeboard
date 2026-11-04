// The app's icons: AFFiNE's own set (@blocksuite/icons, MIT), at one size.
// Folders keep a yellow, open/closed pair of their own.
import {
  AllDocsIcon,
  DuplicateIcon,
  EditIcon,
  UnlinkIcon,
  ArrowRightSmallIcon,
  DeleteIcon,
  EdgelessIcon,
  FavoritedIcon,
  FavoriteIcon,
  FolderIcon as AffineFolderIcon,
  LinkedEdgelessIcon,
  LinkedPageIcon,
  McpIcon as AffineMcpIcon,
  MoreHorizontalIcon,
  PageIcon as AffinePageIcon,
  PlusIcon as AffinePlusIcon,
  SearchIcon as AffineSearchIcon,
  SidebarIcon as AffineSidebarIcon,
} from '@blocksuite/icons/rc';
import type { ComponentType, SVGProps } from 'react';

const sized =
  (Icon: ComponentType<SVGProps<SVGSVGElement>>, size = 18) =>
  ({ className }: { className?: string }) => <Icon width={size} height={size} className={className} aria-hidden="true" />;

export const PageIcon = sized(AffinePageIcon);
export const BoardIcon = sized(EdgelessIcon);
export const LinkedPageShortcutIcon = sized(LinkedPageIcon);
export const LinkedBoardShortcutIcon = sized(LinkedEdgelessIcon);
export const McpIcon = sized(AffineMcpIcon);
export const TrashIcon = sized(DeleteIcon);
export const SearchIcon = sized(AffineSearchIcon);
export const DocsIcon = sized(AllDocsIcon);
export const SidebarIcon = sized(AffineSidebarIcon, 20);
export const PlusIcon = sized(AffinePlusIcon, 16);
export const MoreIcon = sized(MoreHorizontalIcon, 16);

/** Chevron is the tree's caret; open turns it down. */
export function Chevron({ open, size = 16 }: { open: boolean; size?: number }) {
  return (
    <ArrowRightSmallIcon
      width={size}
      height={size}
      className={`sb-chevron${open ? ' sb-chevron-open' : ''}`}
      aria-hidden="true"
    />
  );
}

/** StarIcon is the favorites star, filled when the document is one. */
export function StarIcon({ on }: { on: boolean }) {
  const Icon = on ? FavoritedIcon : FavoriteIcon;
  return <Icon width={18} height={18} aria-hidden="true" />;
}

/** FolderIcon is AFFiNE's folder, and an open one drawn to match. */
export function FolderIcon({ open = false }: { open?: boolean }) {
  if (!open) return <AffineFolderIcon width={18} height={18} aria-hidden="true" />;
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M3.75 18.25V6.5c0-.69.56-1.25 1.25-1.25h4.1c.37 0 .72.16.95.44l1.3 1.56h6.15c.69 0 1.25.56 1.25 1.25v1.25"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
      />
      <path
        d="M3.75 18.25 6 11.1c.17-.53.66-.85 1.2-.85h12.55c.84 0 1.44.82 1.19 1.62l-1.95 6.1c-.17.52-.65.88-1.2.88H3.75Z"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
      />
    </svg>
  );
}

/** Icons for menu entries, a little smaller than the tree's. */
export const MenuIcons = {
  rename: sized(EditIcon, 16),
  folder: () => <AffineFolderIcon width={16} height={16} aria-hidden="true" />,
  page: sized(AffinePageIcon, 16),
  board: sized(EdgelessIcon, 16),
  favorite: sized(FavoriteIcon, 16),
  favorited: sized(FavoritedIcon, 16),
  trash: sized(DeleteIcon, 16),
  original: sized(LinkedPageIcon, 16),
  copy: sized(DuplicateIcon, 16),
  unlink: sized(UnlinkIcon, 16),
};

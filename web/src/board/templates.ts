// The template panel (the "more" slot of the edgeless toolbar): AFFiNE's
// board templates and stickers, vendored under web/vendor/affine-templates.
// BlockSuite ships the panel empty and lets the host extend it.
import { EdgelessTemplatePanel, type TemplateManager } from '@blocksuite/affine/gfx/template';

type Loader = () => Promise<{ builtInTemplates: unknown }>;

// lazy defers loading a template set (megabytes of JSON and SVG URLs) until
// the panel first asks for it.
function lazy(load: Loader): TemplateManager {
  let mod: Promise<TemplateManager> | null = null;
  const get = () => (mod ??= load().then(m => m.builtInTemplates as TemplateManager));
  return {
    list: async category => (await get()).list(category),
    categories: async () => (await get()).categories(),
    search: async (keyword, category) => (await get()).search(keyword, category),
  };
}

let registered = false;

export function registerTemplates(): void {
  if (registered) return;
  registered = true;
  EdgelessTemplatePanel.templates.extend(lazy(() => import('../../vendor/affine-templates/stickers-templates.gen')));
  EdgelessTemplatePanel.templates.extend(lazy(() => import('../../vendor/affine-templates/edgeless-templates.gen')));
}

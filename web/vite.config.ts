import { writeFileSync } from 'node:fs';
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

// keepDist re-creates dist/.gitkeep after every build: the Go binary embeds
// web/dist via go:embed, which needs at least one file on a fresh checkout.
// (Same trick as aeman's web/vite.config.ts.)
function keepDist(): Plugin {
  return {
    name: 'aenotes-keep-dist',
    closeBundle() {
      writeFileSync('dist/.gitkeep', '');
    },
  };
}

// Patches to BlockSuite 0.22 that upstream AFFiNE has since made the same
// way; drop each with the upgrade. A patch whose code is not there any more
// fails the build rather than shipping the bug unnoticed. (Only the build is
// patched: `vite dev` prebundles BlockSuite with esbuild, past the plugin.)
// The packages resolve to their TypeScript sources here, their dist elsewhere.
const BLOCKSUITE_PATCHES: Array<{ file: RegExp; what: string; from: RegExp; to: string }> = [
  {
    // Pasting on a board (a copied note) while a text selection lingers
    // built a text paste from a slice with no blocks and threw on
    // content[0], leaving the editor without a working cursor.
    file: /@blocksuite\/affine-shared\/(src|dist)\/adapters\/middlewares\/paste\.(ts|js)$/,
    what: 'the paste guard',
    from: /(const text = std\.selection\.find\(TextSelection\);\s*if \(!text)\)/,
    to: '$1 || payload.snapshot.content.length === 0)',
  },
  {
    // Firefox throws from Range.comparePoint / selectNode on the inline
    // editor's points, so beforeinput bailed out and the browser edited the
    // DOM itself: Enter broke the line inside a list item instead of making
    // a new one, and undo lost track. Upstream checks containment instead.
    file: /@blocksuite\/std\/(src|dist)\/inline\/services\/event\.(ts|js)$/,
    what: 'the inline range check',
    from: /const rootRange = document\.createRange\(\);\s*rootRange\.selectNode\(rootElement\);[\s\S]*?rootRange\.comparePoint\(range\.startContainer, range\.endOffset\) <= 0\s*\);\s*\}/,
    to: `const containsStart =
      range.startContainer === rootElement ||
      rootElement.contains(range.startContainer);
    const containsEnd =
      range.endContainer === rootElement ||
      rootElement.contains(range.endContainer);
    return containsStart && containsEnd;`,
  },
];

function patchBlockSuite(): Plugin {
  return {
    name: 'aenotes-patch-blocksuite',
    enforce: 'pre',
    transform(code, id) {
      const path = id.replace(/\\/g, '/').split('?')[0];
      const p = BLOCKSUITE_PATCHES.find(x => x.file.test(path));
      if (!p) return null;
      if (!p.from.test(code)) this.error(`${id}: ${p.what} no longer applies; check the BlockSuite upgrade`);
      return code.replace(p.from, p.to);
    },
  };
}

// During `vite dev`, /api/* (REST and the sync WebSocket) is proxied to the
// aenotes Go binary.
export default defineConfig({
  plugins: [react(), vanillaExtractPlugin(), keepDist(), patchBlockSuite()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
  esbuild: { target: 'es2022' },
  optimizeDeps: { esbuildOptions: { target: 'es2022' } },
  server: {
    port: 5174,
    proxy: { '/api': { target: 'http://127.0.0.1:8766', ws: true } },
  },
});

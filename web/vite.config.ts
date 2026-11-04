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

// fixPasteMiddleware patches BlockSuite 0.22's paste middleware: pasting
// on a board (a copied note) while a text selection lingers builds a text
// paste from a slice with no blocks and throws on content[0], which leaves
// the editor without a working cursor. Upstream AFFiNE has since guarded
// it the same way; drop this with the upgrade. A missing pattern fails the
// build rather than shipping the bug unnoticed.
function fixPasteMiddleware(): Plugin {
  // The package resolves to its TypeScript sources here, its dist elsewhere.
  const file = /@blocksuite\/affine-shared\/(src|dist)\/adapters\/middlewares\/paste\.(ts|js)$/;
  const guard = /(const text = std\.selection\.find\(TextSelection\);\s*if \(!text)\)/;
  return {
    name: 'aenotes-fix-paste-middleware',
    enforce: 'pre',
    transform(code, id) {
      if (!file.test(id.replace(/\\/g, '/').split('?')[0])) return null;
      if (!guard.test(code)) this.error(`${id}: the paste guard no longer applies; check the BlockSuite upgrade`);
      return code.replace(guard, '$1 || payload.snapshot.content.length === 0)');
    },
  };
}

// During `vite dev`, /api/* (REST and the sync WebSocket) is proxied to the
// aenotes Go binary.
export default defineConfig({
  plugins: [react(), vanillaExtractPlugin(), keepDist(), fixPasteMiddleware()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
  esbuild: { target: 'es2022' },
  // affine-shared is not prebundled in dev, so the paste fix applies there too.
  optimizeDeps: { esbuildOptions: { target: 'es2022' }, exclude: ['@blocksuite/affine-shared'] },
  server: {
    port: 5174,
    proxy: { '/api': { target: 'http://127.0.0.1:8766', ws: true } },
  },
});

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

// During `vite dev`, /api/* (REST and the sync WebSocket) is proxied to the
// aenotes Go binary.
export default defineConfig({
  plugins: [react(), vanillaExtractPlugin(), keepDist()],
  build: { outDir: 'dist', emptyOutDir: true, target: 'es2022' },
  esbuild: { target: 'es2022' },
  optimizeDeps: { esbuildOptions: { target: 'es2022' } },
  server: {
    port: 5174,
    proxy: { '/api': { target: 'http://127.0.0.1:8766', ws: true } },
  },
});

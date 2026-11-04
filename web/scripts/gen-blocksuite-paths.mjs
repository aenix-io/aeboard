// Generates tsconfig.blocksuite.json: TypeScript path mappings from every
// @blocksuite/* export to its published dist/*.d.ts.
//
// The packages export their src/*.ts, so without this `tsc` type-checks
// BlockSuite's own sources under our compiler settings (and fails on code
// that is fine in its own build). Mapping to the .d.ts lets skipLibCheck
// skip them, and `tsc` checks only aenotes's code. Vite still bundles src.
//
// usage (from web/): node scripts/gen-blocksuite-paths.mjs
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const root = 'node_modules/@blocksuite';
const paths = {};
for (const name of readdirSync(root).sort()) {
  const dir = join(root, name);
  const pkgFile = join(dir, 'package.json');
  if (!existsSync(pkgFile)) continue;
  const pkg = JSON.parse(readFileSync(pkgFile, 'utf8'));
  for (const [key, target] of Object.entries(pkg.exports ?? {})) {
    if (typeof target !== 'string' || !target.startsWith('./src/') || !target.endsWith('.ts')) continue;
    const dts = target.replace(/^\.\/src\//, './dist/').replace(/\.ts$/, '.d.ts');
    if (!existsSync(join(dir, dts))) continue;
    const spec = `@blocksuite/${name}${key === '.' ? '' : key.slice(1)}`;
    paths[spec] = [join(dir, dts)];
  }
}
writeFileSync(
  'tsconfig.blocksuite.json',
  JSON.stringify({ compilerOptions: { baseUrl: '.', paths } }, null, 2) + '\n',
);
console.log(`mapped ${Object.keys(paths).length} @blocksuite exports`);

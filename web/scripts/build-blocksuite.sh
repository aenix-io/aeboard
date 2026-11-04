#!/usr/bin/env bash
# Builds the BlockSuite packages from a pinned AFFiNE commit into npm
# tarballs under web/vendor/blocksuite/, which package.json installs from.
#
# BlockSuite stopped going to npm after 0.22.4 (July 2025); it lives on in
# the AFFiNE monorepo under blocksuite/ (MIT, see LICENSE-MIT there). Only
# blocksuite/, tools/@types and the root workspace files are checked out:
# nothing from AFFiNE's packages/ (other licenses) is fetched or used.
#
# The tarballs look like the old npm releases: src/*.ts (what Vite bundles,
# the exports point there) plus dist/*.d.ts (what our tsc reads, see
# gen-blocksuite-paths.mjs), with workspace:* resolved to the version below.
#
# usage (from web/): scripts/build-blocksuite.sh   (needs git, node 22, network)
# To upgrade: change AFFINE_TAG/AFFINE_SHA, run this, then `npm install`.
set -euo pipefail

AFFINE_REPO=https://github.com/toeverything/AFFiNE.git
AFFINE_TAG=v0.27.4
AFFINE_SHA=b4c8548c09da21b2898443559a5b846f0ccf5dd8
VERSION=${AFFINE_TAG#v}

web=$(cd "$(dirname "$0")/.." && pwd)
out=$web/vendor/blocksuite
work=${BLOCKSUITE_WORKDIR:-$(mktemp -d -t blocksuite-build)}
src=$work/affine
echo "building BlockSuite $AFFINE_TAG ($AFFINE_SHA) in $work"

if [ ! -d "$src/.git" ]; then
  git clone --quiet --filter=blob:none --no-checkout "$AFFINE_REPO" "$src"
else
  git -C "$src" reset --quiet --hard  # undo the rewrites below of an earlier run
fi
git -C "$src" sparse-checkout set --no-cone \
  '/blocksuite/' '!/blocksuite/docs/' '!/blocksuite/docs-site/' '!/blocksuite/playground/' \
  '/tools/@types/assets/' '/tools/@types/build-config/' '/tsconfig.json' '/tsconfig.web.json' \
  '/yarn.lock' '/.yarnrc.yml' '/.yarn/releases/' '/.yarn/patches/' '/LICENSE-MIT'
git -C "$src" fetch --quiet --depth 1 origin "$AFFINE_SHA"
git -C "$src" checkout --quiet --force "$AFFINE_SHA"
[ "$(git -C "$src" rev-parse HEAD)" = "$AFFINE_SHA" ]

cd "$src"
# A root workspace of just BlockSuite: AFFiNE's own root pulls in Electron,
# the app and the backend, none of which BlockSuite needs. The resolutions
# (nolyfill, the yjs patch) stay as AFFiNE has them.
node --input-type=module - "$VERSION" <<'EOF'
import { execSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
const [version] = process.argv.slice(2);
const root = JSON.parse(execSync('git show HEAD:package.json', { encoding: 'utf8' }));
const keep = ['typescript', '@types/node', '@vanilla-extract/vite-plugin', 'vite', 'vitest'];
writeFileSync('package.json', JSON.stringify({
  name: '@affine/monorepo',
  version,
  private: true,
  packageManager: root.packageManager,
  workspaces: [
    'blocksuite/affine/*', 'blocksuite/affine/*/*', 'blocksuite/framework/*',
    'blocksuite/integration-test', 'tools/@types/assets', 'tools/@types/build-config',
  ],
  devDependencies: Object.fromEntries(keep.map(k => [k, root.devDependencies[k]])),
  resolutions: root.resolutions,
}, null, 2) + '\n');
// tools/@types/env (the app's globals) needs AFFiNE's packages/; BlockSuite
// itself does not use it.
const web = JSON.parse(readFileSync('tsconfig.web.json', 'utf8'));
web.compilerOptions.types = web.compilerOptions.types.filter(t => t !== 'affine__env');
writeFileSync('tsconfig.web.json', JSON.stringify(web, null, 2) + '\n');
// Every package gets the AFFiNE release version (they all say 0.27.0 in
// the monorepo, which is not what they are).
for (const f of execSync("git ls-files 'blocksuite/**/package.json'", { encoding: 'utf8' }).split('\n').filter(f => f && existsSync(f))) {
  const pkg = JSON.parse(readFileSync(f, 'utf8'));
  pkg.version = version;
  writeFileSync(f, JSON.stringify(pkg, null, 2) + '\n');
}
EOF

export YARN_ENABLE_IMMUTABLE_INSTALLS=false YARN_ENABLE_TELEMETRY=0
node .yarn/releases/yarn-*.cjs install --mode=skip-build >"$work/yarn-install.log" 2>&1 \
  || { tail -50 "$work/yarn-install.log"; exit 1; }
# The tests are not packed (files: !src/__tests__) and need test tooling.
find blocksuite -path '*/src/__tests__' -type d -prune -exec rm -rf {} +
# Declarations only: Vite bundles src/, our tsc needs nothing but the .d.ts.
node node_modules/typescript/bin/tsc6 --build blocksuite/integration-test/tsconfig.json \
  --emitDeclarationOnly --declarationMap false --sourceMap false
# tsc's incremental state: machine-specific, of no use to anyone installing.
find blocksuite -path '*/dist/tsconfig.tsbuildinfo' -delete

rm -rf "$out"
mkdir -p "$out"
node .yarn/releases/yarn-*.cjs workspaces foreach --all --no-private --include '@blocksuite/*' \
  pack --out "$out/%s-%v.tgz" >/dev/null
for f in "$out"/@*.tgz; do mv "$f" "$out/${f##*/@}"; done
cp LICENSE-MIT "$out/LICENSE-MIT"
printf 'AFFiNE %s (%s)\n' "$AFFINE_TAG" "$AFFINE_SHA" >"$out/REVISION"

# Point package.json at the tarballs: the two packages aenotes imports as
# dependencies, every one of them as an override (they depend on each other
# by exact version, which is on no registry).
cd "$web"
node --input-type=module - <<'EOF'
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
const pkg = JSON.parse(readFileSync('package.json', 'utf8'));
const tgz = readdirSync('vendor/blocksuite').filter(f => f.endsWith('.tgz')).sort();
const specs = Object.fromEntries(tgz.map(f => [
  '@' + f.replace(/-\d+\.\d+\.\d+\.tgz$/, '').replace('-', '/'),
  `file:vendor/blocksuite/${f}`,
]));
for (const name of ['@blocksuite/affine', '@blocksuite/integration-test']) pkg.dependencies[name] = specs[name];
const others = Object.entries(pkg.overrides ?? {}).filter(([k]) => !(k in specs) && !k.startsWith('@blocksuite/affine'));
pkg.overrides = Object.fromEntries([...others, ...Object.entries(specs)]);
// npm wants an override of a direct dependency to reference it.
for (const name of Object.keys(pkg.dependencies)) if (name in specs && pkg.overrides[name] ===pkg.dependencies[name]) pkg.overrides[name] = `$${name}`;
writeFileSync('package.json', JSON.stringify(pkg, null, 2) + '\n');
// The lock keeps no checksums of the tarballs (git holds them; the .d.ts
// tsc emits are not byte-for-byte reproducible), so a rebuild needs no
// lock surgery beyond this.
const lock = JSON.parse(readFileSync('package-lock.json', 'utf8'));
for (const p of Object.values(lock.packages ?? {})) if (p.resolved?.startsWith('file:vendor/blocksuite/')) delete p.integrity;
writeFileSync('package-lock.json', JSON.stringify(lock, null, 2) + '\n');
console.log(`packed ${tgz.length} packages; now run: npm install`);
EOF
du -sh "$out"

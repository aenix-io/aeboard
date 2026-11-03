# aeboard

A collaborative whiteboard in one Go binary. The board editor is [BlockSuite](https://github.com/toeverything/AFFiNE/tree/canary/blocksuite) in edgeless mode (the MIT `@blocksuite/*` packages), boards sync live between browsers over Yjs through an embedded [ygo](https://github.com/reearth/ygo) server, and every board is stored in a git repository — the same approach as [aeman](https://github.com/aenix-io/aeman).

- Shapes, connectors, text, pen, mind maps, frames, AFFiNE's board templates and stickers.
- Live collaboration: edits, cursors and selections of everyone on the board.
- A board list with tags (click a tag to filter, Shift-click to add more).
- Boards in git: each board is its Yjs state plus a readable JSON dump, committed and pushed in the background; diverged replicas merge without conflicts.

## Run locally

```sh
make build                                   # SPA + binary (needs Node 22+ and Go 1.26+)
./aeboard serve                              # boards as plain files under ./data
./aeboard serve --repo ../my-boards.git      # boards in a local git repository
./aeboard serve --repo https://github.com/<org>/<boards-repo> --branch main
```

Open <http://127.0.0.1:8766>. Like `aeman serve`, a local server acts as the account the `gh` CLI is logged in as: your GitHub login and avatar are shown in the header and on your cursor, and `gh`'s token is used to push to an https `--repo` (or set `AEBOARD_GIT_TOKEN`). `--identity none` runs anonymously with a name chosen in the browser.

| Flag | Env | Default | |
|---|---|---|---|
| `--addr` | `AEBOARD_ADDR` | `127.0.0.1:8766` | listen address |
| `--data` | `AEBOARD_DATA` | `./data` | local state: the git clone (`repo.git`), attachments (`blobs/`), or the boards themselves without `--repo` |
| `--repo` | `AEBOARD_REPO` | — | git repository of the boards: local path or URL |
| `--branch` | `AEBOARD_BRANCH` | `main` | branch the boards live on |
| `--fetch-interval` | | `30s` | how often the repository is checked for other servers' changes |
| `--identity` | `AEBOARD_IDENTITY` | `gh` | `gh` or `none` |

## Develop

```sh
make backend && ./aeboard serve      # API on :8766 (the SPA embedded from web/dist)
cd web && npm run dev                # Vite on :5174, proxies /api to :8766
make test lint test-web
```

End-to-end checks drive real browsers with Playwright against a running server, e.g. `node web/e2e/list.mjs http://127.0.0.1:8766` (see the header of each script in `web/e2e/`).

## Layout

- `cmd/aeboard` — the CLI.
- `internal/server` — HTTP: the REST API (`/api/v1/boards`, attachments, `/me`, sync status), the Yjs sync socket (`/api/v1/sync/<board>`), the embedded SPA.
- `internal/boards` — the board catalogue in memory with write-behind to a backend; the on-disk layout.
- `internal/gitbackend`, `internal/gitstore` — boards in git; the git plumbing is ported from aeman.
- `internal/blobs` — content-addressed attachment store.
- `internal/seed` — the state every new board starts from.
- `web/` — the SPA (React shell + BlockSuite editor); `web/vendor/affine-templates` — AFFiNE's templates and stickers.

Design and decisions: [docs/design/architecture.md](docs/design/architecture.md).

## License

Apache-2.0. Third-party code keeps its license: BlockSuite and AFFiNE's templates are MIT (nothing is taken from AFFiNE's `packages/backend` or `packages/common/native`), `@toeverything/theme` is MPL-2.0 and used unmodified, the git plumbing comes from aeman (Apache-2.0).

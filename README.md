# aenotes

A collaborative whiteboard and pages in one Go binary. The board editor is [BlockSuite](https://github.com/toeverything/AFFiNE/tree/canary/blocksuite) in edgeless mode (the MIT `@blocksuite/*` packages), boards sync live between browsers over Yjs through an embedded [ygo](https://github.com/reearth/ygo) server, and every board is stored in a git repository — the same approach as [aeman](https://github.com/aenix-io/aeman).

- Shapes, connectors, text, pen, mind maps, frames, AFFiNE's board templates and stickers.
- Live collaboration: edits, cursors and selections of everyone on the board.
- Pages next to boards: rich text documents, linked with `@`, embedded in each other (as a card or inline), every document viewable as a board or as a page.
- Pages and boards in folders, browsed like files: breadcrumbs, drag and drop between folders, Move to…, search across every folder, an All / Pages / Boards switch.
- A Notion-style sidebar with the whole tree (hidden until opened from the header; the browser remembers it): expand folders in place, create pages, boards and folders anywhere, drag to move.
- Boards in git: each board is its Yjs state plus a readable JSON dump, committed and pushed in the background; diverged replicas merge without conflicts.

## Run locally

```sh
make build                                   # SPA + binary (needs Node 22+ and Go 1.26+)
./aenotes serve                              # boards as plain files under ./data
./aenotes serve --repo ../my-boards.git      # boards in a local git repository
./aenotes serve --repo https://github.com/<org>/<boards-repo> --branch main
```

Open <http://127.0.0.1:8766>. Like `aeman serve`, a local server acts as the account the `gh` CLI is logged in as: your GitHub login and avatar are shown in the header and on your cursor, and `gh`'s token is used to push to an https `--repo` (or set `AENOTES_GIT_TOKEN`). `--identity none` runs anonymously with a name chosen in the browser.

| Flag | Env | Default | |
|---|---|---|---|
| `--addr` | `AENOTES_ADDR` | `127.0.0.1:8766` | listen address |
| `--data` | `AENOTES_DATA` | `./data` | local state: the git clone (`repo.git`), attachments (`blobs/`), or the boards themselves without `--repo` |
| `--repo` | `AENOTES_REPO` | — | git repository of the boards: local path or URL |
| `--branch` | `AENOTES_BRANCH` | `main` | branch the boards live on |
| `--fetch-interval` | | `30s` | how often the repository is checked for other servers' changes |
| `--identity` | `AENOTES_IDENTITY` | `gh` | `gh` or `none` (local mode only) |

## Hosted mode

With a GitHub client id and secret set, `aenotes serve` is a multi-user server: every visitor signs in with GitHub, their commits are authored as them, and what they may do follows their permission on the board repository (`--repo`), checked with their own token and cached for a minute:

| Permission on `--repo` | Boards |
|---|---|
| none (cannot see it) | 403 everywhere: list, boards, attachments, sync socket |
| read, triage | boards open; the sync socket is read-only, creating, renaming, tagging and uploads are refused |
| write, maintain, admin | everything |

The server pushes with the installation token of the same GitHub App (renewed before its hour runs out); without the App it falls back to `AENOTES_GIT_TOKEN`, then to `gh`.

```sh
AENOTES_BASE_URL=https://boards.example.com \
AENOTES_GITHUB_CLIENT_ID=Iv23... AENOTES_GITHUB_CLIENT_SECRET=... \
AENOTES_GITHUB_APP_ID=123456 AENOTES_GITHUB_APP_KEY="$(base64 < app.private-key.pem)" \
AENOTES_SESSION_KEY="$(openssl rand -hex 32)" \
./aenotes serve --addr :8766 --data /var/lib/aenotes --repo https://github.com/<org>/<boards-repo>
```

| Flag | Env | |
|---|---|---|
| `--base-url` | `AENOTES_BASE_URL` | public origin; the OAuth callback is `<base-url>/auth/callback`, and an https origin makes the cookies `Secure` |
| `--github-client-id` | `AENOTES_GITHUB_CLIENT_ID` | the GitHub App's client id (an OAuth App's works too, with the `repo` scope); with the secret it turns the hosted mode on |
| | `AENOTES_GITHUB_CLIENT_SECRET` | its client secret |
| | `AENOTES_GITHUB_APP_ID` | the App id: the server pushes with its installation token |
| | `AENOTES_GITHUB_APP_KEY` | the App's private key, base64 of the PEM (or the PEM itself) |
| | `AENOTES_SESSION_KEY` | encrypts the session file (AES-256-GCM); without it sessions live in memory and a restart signs everyone out |
| `--session-file` | `AENOTES_SESSION_FILE` | default `<data>/sessions.json` |

Secrets are read from the environment only, so they stay out of `ps`. `GET /api/healthz` answers 200 while the server is serving (for probes).

**GitHub App setup** (one App does both the sign-in and the pushes):

- Callback URL: `https://<host>/auth/callback`. "Expire user authorization tokens" may stay on: sessions renew the 8-hour user token with its refresh token.
- Repository permissions: **Contents: Read and write** (Metadata: read is implied). Account permissions: none are required — the sign-in reads only the login, name and avatar from `GET /user`, which every user token may call; grant **Email addresses: Read-only** only if you want the consent screen to match aeman's App.
- Webhook: off. Install the App on the board repository (or its organisation); the server refuses to start with the install link otherwise.
- Generate a client secret and a private key; the private key goes into `AENOTES_GITHUB_APP_KEY` as base64.

## Develop

```sh
make backend && ./aenotes serve      # API on :8766 (the SPA embedded from web/dist)
cd web && npm run dev                # Vite on :5174, proxies /api to :8766
make test lint test-web
```

End-to-end checks drive real browsers with Playwright against a running server, e.g. `node web/e2e/list.mjs http://127.0.0.1:8766` (see the header of each script in `web/e2e/`).

## Layout

- `cmd/aenotes` — the CLI.
- `internal/server` — HTTP: the REST API (`/api/v1/boards`, attachments, `/me`, sync status), the Yjs sync socket (`/api/v1/sync/<board>`), the embedded SPA; in the hosted mode the sign-in (`/auth/*`), sessions and per-visitor rights.
- `internal/forge` — the code host: GitHub sign-in, a visitor's permission on the board repository, the GitHub App installation token.
- `internal/boards` — the board catalogue in memory with write-behind to a backend; the on-disk layout.
- `internal/gitbackend`, `internal/gitstore` — boards in git; the git plumbing is ported from aeman.
- `internal/blobs` — content-addressed attachment store.
- `internal/seed` — the state every new board starts from.
- `web/` — the SPA (React shell + BlockSuite editor); `web/vendor/affine-templates` — AFFiNE's templates and stickers.

Design and decisions: [docs/design/architecture.md](docs/design/architecture.md).

## License

Apache-2.0. Third-party code keeps its license: BlockSuite and AFFiNE's templates are MIT (nothing is taken from AFFiNE's `packages/backend` or `packages/common/native`), `@toeverything/theme` is MPL-2.0 and used unmodified, the git plumbing, the sign-in, sessions, rights and the forge code come from aeman (Apache-2.0).

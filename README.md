# VaultDock server (vad-server)

The server of VaultDock: self-hosted file storage and intelligent synchronisation. Run it on your own hardware, connect your Windows PCs, choose exactly which folders and files to sync, and let visual automations decide where everything goes.

- **Web app**: file browser with previews and streamed video playback, uploads, trash, search and tags, devices, sync overview, activity, and an admin console.
- **Windows desktop app**: tray app that watches the folders you select and uploads changes in the background with resumable, verified, adaptively parallel transfers.
- **Visual automation builder**: node-based workflows (triggers, AND/OR conditions, branches, actions) with JSON import/export, a variable reference, AI-friendly schema docs and dry runs.
- **Secure by design**: HTTPS, device approval, Ed25519 per-device keys stored with Windows DPAPI, signed and replay-protected requests, strict per-user isolation.

> Example use: sync Medal gaming clips into `/Medal/<game>/<year-month>/`, skip temporary recordings, tag them by game, and watch them in the browser.

## Install (Linux server with Docker)

```bash
curl -fsSL https://raw.githubusercontent.com/ASPEC-RACING-PTY-LTD/vad-server/main/install.sh | bash
```

The installer shows a menu: **1) Install / Replace, 2) Upgrade, 3) Uninstall, 4) Automatic updates**. It installs the latest release into `~/VaultDock` (or `/root/VaultDock` as root): only the server's runtime files go into `~/VaultDock/app`, together with a complete, pre-filled `.env` (generated setup token included). It asks where to store files (point it at your HDD) and how the server is reached (Cloudflare Tunnel or reverse proxy, built-in Caddy, self-signed, or plain HTTP), pulls the released image `ghcr.io/aspec-racing-pty-ltd/vad-server:latest` (or, before the first release, builds it from a temporary copy of the source), starts the container on port 3000, and waits until it is healthy.

Then open the printed URL, create the administrator with the setup token, and install the desktop app.

### Prefer to inspect first? (recommended)

Piping a script from the internet into a shell runs it with your privileges before you have read it. To review it first:

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/ASPEC-RACING-PTY-LTD/vad-server/main/install.sh
less install.sh
bash install.sh
```

Installs always follow the newest release: `:latest` on the production channel, or `:dev` with `VAULTDOCK_CHANNEL=development` (kept in `.env`). Versions are never pinned. To build from a local source tree instead, run it with `VAULTDOCK_SOURCE=/path/to/vad-server`.

What the installer does and does not do:

- Writes only inside the install directory, plus the storage path you choose.
- Needs Docker Engine with the Compose plugin; it does not install system packages.
- Never deletes stored files, the database or settings unless you choose full uninstall **and** type `DELETE ALL DATA`.
- Upgrades back up the database, `.env` and application first, and roll back automatically if the new version does not become healthy.

Non-interactive use: `VAULTDOCK_YES=1 bash install.sh install` (defaults are used; destructive actions still require a terminal).

## Windows desktop app

The Windows app lives in its own repository, [ASPEC-RACING-PTY-LTD/vad-client](https://github.com/ASPEC-RACING-PTY-LTD/vad-client). Download `VaultDock-<version>-amd64-setup.exe` from its releases (or see [docs/desktop.md](docs/desktop.md)). Installing needs administrator rights once and offers two modes: **Simple Folder Backups** and **Full Access (Advanced)**.

1. Enter your server URL, username and password.
2. The app shows a verification code. In the web interface open **Devices**, check the code, and approve.
3. Pick the folders to back up or sync, preview what will upload, and you're done.

The password is used once to request access and is never stored. Revoking a device in the web interface stops it immediately.

Backups are encrypted on the PC before upload. By default the server also holds the backup keys (full escrow), so an administrator can browse and restore them from the web app; a repository can instead be set to metadata escrow, where the server sees names but never file contents. See [docs/security.md](docs/security.md).

## Updates

- **Desktop app**: updates itself from the GitHub releases of `ASPEC-RACING-PTY-LTD/vad-client`. Installers are verified against their published SHA-256 checksum and installed while nothing is syncing.
- **Server**: the admin console shows new releases. Upgrade with `~/VaultDock/app/install.sh upgrade`, or turn on nightly updates with `~/VaultDock/app/install.sh auto-update on`. Every upgrade backs up first and rolls back automatically if the new version is unhealthy.
- **Releases**: a push to `main` whose commit message contains `DEPLOY: Production` or `DEPLOY: Development` runs the Release workflow; any other push releases nothing. See [Releases](docs/operations.md#releases) for the version rules and what each channel publishes. The Windows app is released from the `vad-client` repository.

## Documentation

| Topic | |
|---|---|
| [Deployment](docs/deployment.md) | Docker, storage disks, HTTPS options, reverse proxies, backups |
| [Upgrade, uninstall & troubleshooting](docs/operations.md) | Day-two operations and common problems |
| [Architecture](docs/architecture.md) | Components, data model, sync engine, adaptive concurrency |
| [Security](docs/security.md) | Threat model, transport, device keys, isolation, end-to-end encryption trade-offs |
| [Automations](docs/automations.md) | Schema, variables, templates and how to generate automations with an AI assistant |
| [Desktop app](docs/desktop.md) | Building, installer, updates, logs (the app itself is in the `VaultDock` repository) |
| [Development](docs/development.md) | Running locally, tests |

## Repository layout

```
cmd/vaultdock        server entry point (serve, healthcheck, backup, reset-password)
internal/            server: API, storage, object store, database, auth, config
deploy/              env schema, managed files and opt-in integrations used by install.sh
web/                 React web interface (embedded into the server binary)
testkit/             helpers for tests that run against a real server
tests/integration    end-to-end tests against a running deployment
scripts/             development and installer test scripts
install.sh           installer / upgrader / uninstaller
Dockerfile, docker-compose.yml
```

Shared code (automation engine, API client, request signing, backup format) is the Go module `github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk` from the `VaultDock` repository. A copy lives in `sdk/` so this repository builds on its own; after changing the SDK in VaultDock, run `scripts/sync-sdk.sh` and commit.

## Licence

vad-server is source-available under the [PolyForm Noncommercial License 1.0.0](LICENSE).

- **Noncommercial use is permitted**: personal use, hobby projects, research, and use by
  charities, educational institutions, public research, public safety or health
  organisations and government institutions.
- **Commercial use requires a separate licence.** Contact ASPEC Racing Pty Ltd via
  <https://github.com/ASPEC-RACING-PTY-LTD> to arrange one.
- Redistribution must keep the `LICENSE` file (or its URL) and the line
  `Required Notice: Copyright 2026 ASPEC Racing Pty Ltd (https://github.com/ASPEC-RACING-PTY-LTD)`.

Third-party components keep their own licences; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Credits

vad-server is built on these open-source projects. Thank you to their authors and contributors.

| Project | Licence | What it does in vad-server |
|---|---|---|
| [go-chi/chi](https://github.com/go-chi/chi) | MIT | HTTP routing and middleware |
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (with modernc.org/libc) | BSD-3-Clause (SQLite: public domain) | Server database without cgo |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) | BSD-3-Clause | Argon2id password hashing |
| [golang.org/x/time](https://pkg.go.dev/golang.org/x/time) | BSD-3-Clause | Request rate limiting |
| [oschwald/maxminddb-golang](https://github.com/oschwald/maxminddb-golang) | ISC | Approximate location of sign-in IP addresses (GeoIP database reader) |
| [React](https://react.dev/) and [React Router](https://reactrouter.com/) | MIT | Web interface and routing |
| [TanStack Query](https://tanstack.com/query) | MIT | Data fetching and caching in the web interface |
| [xyflow (React Flow)](https://xyflow.com/) (with d3 modules) | MIT (d3: ISC, BSD-3-Clause) | Node editor for automation workflows |
| [Lucide](https://lucide.dev/) | ISC | Icons |
| [Tailwind CSS](https://tailwindcss.com/) | MIT | Styling of the web interface |

The full list of incorporated components, including indirect ones, with their licence
texts is in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

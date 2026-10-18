# Windows desktop app

## Building

Requirements (build machine only; end users need nothing): Go 1.26+, Node.js 22+, the Wails CLI v2 and NSIS.

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
winget install NSIS.NSIS
cd desktop
wails build -clean -nsis -trimpath -ldflags "-X main.version=0.1.0"
```

Outputs in `desktop/build/bin/`:

- `VaultDock.exe`: the application.
- `VaultDock-<version>-amd64-setup.exe`: per-user installer (no admin rights). It installs WebView2 if missing, creates Start menu and desktop shortcuts, and registers an uninstaller.

Version: set `info.productVersion` in `desktop/wails.json` and `-X main.version`. For releases, sign both executables (hooks are in `build/windows/installer/project.nsi`).

## Behaviour

- Runs in the notification area; closing the window keeps it syncing (configurable). Tray menu: open, sync all now, pause/resume, open web interface, quit.
- **Start with Windows** (default on after connecting) adds `HKCU\...\Run\VaultDock` with `--minimized`.
- Single instance: launching it again shows the existing window.
- Settings: parallel transfers (automatic with an upper limit, or fixed), bandwidth limit, notifications.

## Data on the PC

| Path | Contents |
|---|---|
| `%APPDATA%\VaultDock\settings.json` | Server URL, pinned certificate, device id, preferences (no secrets) |
| `%APPDATA%\VaultDock\device.key` | Ed25519 private key encrypted with DPAPI for the current Windows user |
| `%APPDATA%\VaultDock\state.db` | Sync configurations, file index, persistent upload queue, history |
| `%LOCALAPPDATA%\VaultDock\logs\` | JSON logs, rotated at 10 MB (3 kept) |

Uninstalling asks whether to remove these; your own files and everything on the server are never touched.

## Updates

The app keeps itself up to date from the GitHub releases of `ASPEC-RACING-PTY-LTD/vad-client` (override at build time with `-X main.UpdateRepo=owner/name`; an empty value disables updates).

- It checks two minutes after start and then every six hours. **Settings → Updates → Check now** checks immediately.
- A newer installer is downloaded only over HTTPS from `github.com` (redirects are limited to GitHub's asset hosts), into `%LOCALAPPDATA%\VaultDock\updates`.
- The download is verified against the SHA-256 digest GitHub publishes for the asset, or the release's `SHA256SUMS` file. A release without a checksum is never installed. The file is hashed again immediately before it runs.
- With **Install updates automatically** on (the default), the update is installed once nothing is uploading. Otherwise Settings shows **Install and restart**.
- Installing stops the sync engine cleanly, then runs the installer silently (`/S /RELAUNCH /D=<current folder>`). It replaces the app in place and starts it again in the notification area. Settings, the device key and sync state are kept.

Development builds (version `dev`) never update themselves.

## Publishing a release

The desktop app is released from the `ASPEC-RACING-PTY-LTD/vad-client` repository: push a version tag there and its **Release** workflow builds the Windows installer and publishes the GitHub release with `SHA256SUMS`. The server is released from this repository (`ASPEC-RACING-PTY-LTD/vad-server`) the same way; its Release workflow publishes the multi-architecture image and the deploy assets:

```bash
ch tag v1.2.0
ch push origin v1.2.0
```

Desktop apps, `install.sh upgrade` and the download page on vaultdock.dev all use the latest release of their repository, so nothing else needs updating. Make the `ghcr.io/aspec-racing-pty-ltd/vad-server` package public once so servers can pull the image; if it is not reachable, the installer builds from source instead.

## Connecting to different servers

The server URL is entered on first run; nothing is hardcoded. Disconnect (Settings) removes the device key; connecting to a different server or account resets the local upload index so files are evaluated again for the new destination.

# Upgrade, uninstall and troubleshooting

## Upgrade

The admin console shows when a new release is available (**Admin console → Updates**, and a banner on the overview). The server checks GitHub releases every 12 hours; set `VAULTDOCK_UPDATE_CHECK=false` to turn this off.

```bash
~/VaultDock/app/install.sh upgrade             # or choose 2 in the menu
~/VaultDock/app/install.sh upgrade --if-newer  # only if a newer release exists
```

1. Backs up the database (online, consistent), `.env` and the application into `~/VaultDock/backups/upgrade-<timestamp>/`.
2. Downloads the latest release (or `VAULTDOCK_REF`), pulls its prebuilt image `ghcr.io/aspec-racing-pty-ltd/vad-server` (building from source if the image is unavailable), and restarts the container. Database migrations run automatically on start and are applied in a transaction each.
3. Waits for the health check. If the new version is not healthy, it restores the previous application, image and database backup and starts the old version again.

User accounts, files, device authorisations, automations and settings are all in the database and storage directories, which upgrades never replace.

A newer database can't be opened by an older VaultDock: the server refuses to start rather than risk damage. Restore the matching backup if you need to downgrade.

## Automatic updates

```bash
~/VaultDock/app/install.sh auto-update on      # daily at 04:00
~/VaultDock/app/install.sh auto-update status  # schedule, installed version, last log lines
~/VaultDock/app/install.sh auto-update off
```

This adds a cron entry for the installing user that runs `upgrade --if-newer` every night, with the same backup and automatic rollback as a manual upgrade. Output goes to `~/VaultDock/logs/auto-update.log`. Only one upgrade runs at a time. The admin console shows whether automatic updates are on. Uninstalling removes the cron entry.

Windows desktop apps update themselves; see [Desktop app](desktop.md#updates).

## Uninstall

`install.sh uninstall` offers:

1. **Remove services only**: stops and removes the container and image. Every file, the database, settings and backups remain in `~/VaultDock` (and your storage path). Run *Install* again later to continue where you left off.
2. **Remove everything**: also deletes stored files, the database, settings and backups. Requires typing `DELETE ALL DATA` in an interactive terminal; `VAULTDOCK_YES` never confirms this.

## Replacing an installation

Running *Install* on an existing installation offers to reinstall keeping data (same as upgrade), or to move the old data and `.env` aside into `backups/replaced-<timestamp>/` and start fresh. Nothing is deleted.

## Command-line maintenance

```bash
docker exec vaultdock vaultdock schema               # schema version + integrity check
docker exec vaultdock vaultdock backup /state/x.db   # online database backup
docker exec -i vaultdock vaultdock reset-password admin   # reads the new password from stdin
docker logs -f vaultdock                             # JSON logs
```

The admin console's **System health** page can run an integrity check and maintenance (expire uploads, purge trash past retention, remove orphaned files).

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Installer: "Cannot talk to the Docker daemon" | Start Docker, or run with `sudo`, or add your user to the `docker` group and log in again. |
| Container restarts, logs say `permission denied` on `/state` or `/storage` | Directory ownership must match `VAULTDOCK_UID:GID` in `.env`. `sudo chown -R 65532:65532 <paths>` (or your UID). |
| Logs: `storage location unavailable ... marker missing` | The disk isn't mounted (VaultDock never writes to an empty mount point). Mount it and restart. |
| Uploads fail with "low on disk space" (507) | Free space, lower the reserve under Admin → Storage, or add a disk. |
| Setup page says the token is wrong | Use `VAULTDOCK_SETUP_TOKEN` from `.env`. |
| Desktop app: "certificate is not trusted" | Use a real certificate (Caddy / your proxy), or confirm the self-signed fingerprint from the server logs. |
| Desktop app: "server refused the connection" | Check URL and port, firewall, that the container is running (`docker ps`). |
| Desktop app: clock error | Request signatures allow 5 minutes of clock difference; the app corrects small offsets automatically. Enable Windows time sync. |
| Desktop app returns to the connect screen | The device was revoked or the account disabled. Reconnect and approve it again. |
| Behind a proxy, every user appears with the proxy's IP | Set `VAULTDOCK_TRUSTED_PROXIES`. |
| Uploads through a proxy stall at 1 MB | Raise the proxy's request body limit (`client_max_body_size 128m` in nginx). |
| Video won't play in the browser | MP4 (H.264/AAC) and WebM play everywhere; MKV/HEVC depends on the browser. Download the file instead. |

Desktop logs are in `%LOCALAPPDATA%\VaultDock\logs` (Settings → Open logs folder).

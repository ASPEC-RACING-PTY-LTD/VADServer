# Deployment

VaultDock runs as a single container (`vaultdock`). `install.sh` installs only what the server needs to run: the release's prebuilt image (or an image built from a temporary copy of the source, which is deleted afterwards) and a small `app/` folder. This page explains what it configures so you can adapt it.

## Requirements

- Linux host (x86-64 or arm64) with Docker Engine and the Compose plugin.
- Disk space for your files on any filesystem (HDD, SSD, RAID, NAS mount). Storage never needs to live on the OS drive.
- For remote access: a domain or a reverse proxy with HTTPS.

Development and testing work on Windows with Docker Desktop.

## Files and directories

| Host path (default) | Container | Contents |
|---|---|---|
| `~/VaultDock/app/.env` | (env file) | Complete configuration with every option, including the generated setup token. Mode 600. |
| `~/VaultDock/app/docker-compose.yml` | | Service definition (plus your optional `docker-compose.override.yml`) |
| `~/VaultDock/app/install.sh` | | Upgrade, automatic updates, status and uninstall |
| `~/VaultDock/data/state` | `/state` | SQLite database, self-signed TLS material, Caddy certificates |
| storage path you choose | `/storage/default` | Content-addressed file blobs and in-progress uploads |
| `~/VaultDock/backups` | | Database, `.env` and app backups made by upgrades |

VaultDock publishes on port **3000** of the host by default (`VAULTDOCK_PORT`); inside the container it listens on 8080.

User files are **never** stored in the container layer: the container root filesystem is read-only, and all data lives in bind mounts.

Inside a storage location VaultDock keeps:

```
.vaultdock-location        marker; if missing VaultDock treats the disk as unmounted and refuses uploads
blobs/<user-id>/aa/bb/<sha256>
tmp/<upload-id>.part
```

File names never reach the disk (blobs are addressed by hash), so manipulated names cannot escape the storage directory. Identical content uploaded twice by the same user is stored once.

## Adding more disks

1. Mount the disk on the host, e.g. `/mnt/hdd2`.
2. Create `app/docker-compose.override.yml` next to `docker-compose.yml`:

   ```yaml
   services:
     vaultdock:
       volumes:
         - /mnt/hdd2/vaultdock:/storage/hdd2
   ```

3. `mkdir -p /mnt/hdd2/vaultdock && chown 65532:65532 /mnt/hdd2/vaultdock` (or the UID in `.env`), then `./install.sh upgrade` or `docker compose ... up -d`.
4. In **Admin console → Storage → Add location** enter `/storage/hdd2`. Assign users to it under **Users**; new uploads go there, existing files stay where they are.

Only paths below `VAULTDOCK_STORAGE_ROOTS` (default `/storage`) can be registered, so an administrator account cannot point VaultDock at system directories.

## Low disk space

Each location has a *reserve* (keep-free amount) and the platform has a global minimum (Admin → Settings). Uploads that would cross either limit, including space already promised to in-progress uploads, are refused with HTTP 507 and devices retry later. Health and storage pages show free space and warn when it runs low.

## HTTPS

The desktop app requires HTTPS for anything other than `localhost`. Pick one:

1. **Built-in Caddy** (automatic Let's Encrypt): set `COMPOSE_PROFILES=caddy` and `VAULTDOCK_DOMAIN=vault.example.com` in `.env`. Ports 80 and 443 must reach the host. VaultDock itself then binds to 127.0.0.1.
2. **Cloudflare Tunnel or your own reverse proxy** (installer option 1): VaultDock listens on `127.0.0.1:3000` only and trusts forwarded headers from the host and Docker networks. Point a Cloudflare Tunnel public hostname (for example `app.example.com`) at `http://127.0.0.1:3000`. For nginx, Traefik or Caddy, forward to the same address, pass `X-Forwarded-For` and `X-Forwarded-Proto`, allow request bodies of at least 64 MB and disable proxy buffering for uploads. Set `VAULTDOCK_TRUSTED_PROXIES` to the proxy's address so client IPs and HTTPS detection are correct. Example nginx:

   ```nginx
   location / {
       proxy_pass http://127.0.0.1:3000;
       proxy_set_header Host $host;
       proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
       proxy_set_header X-Forwarded-Proto $scheme;
       client_max_body_size 128m;
       proxy_request_buffering off;
       proxy_read_timeout 600s;
   }
   ```

3. **Self-signed certificate** (LAN without a domain): `VAULTDOCK_TLS_SELF_SIGNED=true` and `VAULTDOCK_TLS_HOSTS=192.168.1.20,vault.lan`. The certificate's SHA-256 fingerprint is printed in `docker logs vaultdock`. The desktop app shows the fingerprint it received; confirm they match and the app pins that exact certificate.

You can also provide your own certificate with `VAULTDOCK_TLS_CERT` / `VAULTDOCK_TLS_KEY` (paths inside the container, e.g. under `/state`).

No VPN is needed in any of these setups.

## Configuration reference

See `.env.example`. Important values:

| Variable | Default | Purpose |
|---|---|---|
| `VAULTDOCK_SETUP_TOKEN` | generated | Required to create the first administrator |
| `VAULTDOCK_PUBLIC_URL` | | Shown in messages |
| `VAULTDOCK_TRUSTED_PROXIES` | none | CIDRs allowed to set forwarded headers |
| `VAULTDOCK_COOKIE_SECURE` | auto | Force secure cookies |
| `VAULTDOCK_CHUNK_SIZE_MB` | 8 | Upload chunk size |
| `VAULTDOCK_MAX_CONCURRENT_CHUNKS` | 64 | Server-side concurrent chunk writes; clients are told to back off above this |
| `VAULTDOCK_UPLOAD_TTL_HOURS` | 48 | How long an interrupted upload can be resumed |
| `VAULTDOCK_GEOIP_DB` | none | Optional `.mmdb` city database for device request locations |

Platform settings (registration, default quota, max file size, trash and activity retention, device request expiry, free-space floor) are edited in **Admin console → Settings**.

## Backups

- Database: `docker exec vaultdock vaultdock backup /state/backup.db` makes a consistent online copy (SQLite `VACUUM INTO`). Upgrades do this automatically.
- Files: back up the storage path(s) with any file-level tool; blobs are immutable once written, so incremental backups work well.
- Keep `.env` with the backup.

Restore: stop VaultDock, copy `vaultdock.db` into the state directory (delete stale `-wal`/`-shm` files), copy the storage directory back, start VaultDock.

## Manual Docker Compose (without install.sh)

```bash
cp .env.example .env    # edit it
mkdir -p data/state data/storage && sudo chown -R 65532:65532 data
docker compose up -d --build
docker compose logs -f
```

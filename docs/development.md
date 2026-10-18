# Development

## Server and web UI

```powershell
cd web; npm install; npm run build; cd ..
powershell -File scripts/dev-server.ps1      # http://127.0.0.1:8080, data in .dev-data/
cd web; npm run dev                           # optional: Vite on :5173 with API proxy
```

`scripts/dev-server.ps1` uses setup token `dev-setup-token`.

## Desktop app

```powershell
cd desktop
wails dev        # app window + http://localhost:34115 with live Go bindings
```

Set `APPDATA`/`LOCALAPPDATA` to a temporary folder first to keep test data separate from a real installation.

## Tests

```powershell
go test ./...                                  # automation engine, server API (auth, devices, uploads, quotas, isolation, trash, restart)
cd desktop; go test ./...                      # filters, adaptive concurrency simulations, sync engine end-to-end against an in-process server
cd web; npx tsc -b --noEmit
```

Integration tests against a deployed instance (Linux binary, run where the server is reachable):

```bash
GOOS=linux go test -c -tags integration -o it.test ./tests/integration
VAULTDOCK_URL=http://127.0.0.1:8080 VAULTDOCK_SETUP_TOKEN=... ./it.test -test.v -test.run Phase1
docker restart vaultdock
VAULTDOCK_URL=http://127.0.0.1:8080 ./it.test -test.v -test.run Phase2
```

Phase 1 covers setup, registration, device rejection/approval, a 300 MiB upload through a proxy that repeatedly drops connections, resumable uploads, deduplication, quotas, isolation, path traversal, range requests and automation import/validate/export/dry-run. Phase 2 verifies files (by hash), device credentials and automations after a restart and runs a database integrity check.

## Regenerating icons

The master logo is `assets/brand/logo.png`. After changing it, run `python scripts/make_icons.py` (needs Pillow) to regenerate the desktop app, installer and tray icons and the web app favicon and logo.

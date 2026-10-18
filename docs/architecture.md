# Architecture

## Technology choices

| Part | Choice | Why |
|---|---|---|
| Server | Go, single static binary | Low memory, efficient streaming I/O (`http.ServeContent` gives range requests without buffering), simple deployment |
| Metadata | SQLite (pure-Go `modernc.org/sqlite`, WAL) | Zero-administration, transactional, fast for a personal server; no separate DB container |
| Files | Content-addressed blobs on configurable locations | Path-traversal-proof, atomic finalisation by rename, per-user deduplication, rename/move are metadata-only |
| Web UI | React + TypeScript + Vite + Tailwind, React Flow | Rich interactive UI; embedded into the server binary |
| Desktop | Wails v2 (Go + WebView2) | Native Windows app, small installer, and the sync engine is Go so it **shares the automation engine, validator and API client with the server** |
| Container | Distroless static, non-root, read-only root FS | Minimal attack surface (~26 MB image) |

Alternatives considered: PostgreSQL (unnecessary operational weight for the target scale), Electron/.NET desktop (larger footprint, or a second implementation of the automation engine), tus/S3-style upload protocols (a small purpose-built chunk protocol integrates device signing and server-side capacity signalling more simply).

## Components

```
Browser ──HTTPS──┐                        ┌── /state   (SQLite: users, nodes, blobs, devices, uploads, transfers, automations, activity)
                 ├─► VaultDock server ────┤
Desktop app ─────┘   (API + web UI)       └── /storage/* (blobs/, tmp/)
  └ sync engine: scanner, watcher, queue (SQLite), adaptive uploader, automation runtime
```

### Server (`internal/`)

- `api`: HTTP handlers, authentication middleware (session cookie + CSRF token, or Ed25519 device signature), rate limiting, maintenance jobs.
- `store`: schema migrations and queries. Every file query is scoped by `user_id`; the file tree is a `nodes` table (parent/child) with a case-insensitive unique name per folder.
- `storage`: location management, free-space checks, blob placement under per-hash locks, orphan cleanup.
- `auth`: Argon2id, tokens, rate limiters, failed-login lockout, nonce cache.

### Upload protocol

1. `POST /uploads {path, size, sha256}`: quota and free-space checks; if the user already stores that hash the file is linked instantly (deduplication); if an open session for the same path/content exists it is returned (resume).
2. `PUT /uploads/{id}/chunks/{n}`: 8 MiB chunks in any order and in parallel, each hash-verified and fsynced before it is recorded. When too many chunk writes are in flight the server answers `503` + `Retry-After` (capacity signal).
3. `POST /uploads/{id}/complete`: verifies every chunk arrived, hashes the assembled file, compares with the declared hash (detects files modified during upload), renames it into the blob store and inserts the metadata in one transaction. Uploads interrupted during finalisation are reset on restart and simply completed again.

Replaced files go to the trash (configurable) rather than being overwritten.

### Desktop sync engine (`desktop/internal/engine`)

- **Selection**: only configured folders/files; filters for subfolders, types, sizes, globs, dates, hidden files. Whole drives are rejected.
- **Change detection**: `ReadDirectoryChangesW` (recursive) with per-path debouncing and rename pairing, plus periodic incremental rescans comparing size and mtime against a local index, so restarts and missed events are covered.
- **Stability**: files are uploaded only after they stop changing (configurable), are re-checked after hashing, and a hash mismatch at completion requeues the new version.
- **Persistent queue**: SQLite; active items return to pending after a crash. Server sessions are resumed by content hash.
- **Retries**: exponential backoff with jitter; network drops switch the engine to offline mode with backoff; permanent errors (quota, invalid path) become failed items the user can retry.
- **One-way**: local → VaultDock only. Local deletions are recorded (and can trigger automations) but never delete server copies. Two-way sync is intentionally not implemented without a conflict-resolution design.

### Adaptive concurrency

The unit is a parallel upload request (a chunk or a small file); large files use up to four chunk requests each. A controller evaluates 2-second windows:

- **Slow start**: while the pipeline is saturated and throughput rises by ≥5%, the limit doubles.
- **Congestion avoidance**: afterwards it grows by about 12% per window, keeping an increase only if throughput improves.
- **Revert**: if throughput falls after an increase, the previous limit is restored and held for 30 s.
- **Back-off**: HTTP 429/502/503/504, timeouts, or request latency above 4× the observed minimum without throughput gain cut the limit by 30% (multiplicative decrease).
- **Bounds**: user maximum (default 32, up to 128), the server's advertised capacity, and an optional fixed manual value. Hashing is limited to two files at a time to avoid HDD thrashing; an optional bandwidth cap protects the user's uplink. Chunk bodies are streamed from disk, so memory does not grow with concurrency.

The result is 1 connection on a link a single stream already saturates, and dozens on a fast, high-latency link with many small files. See `concurrency_test.go` for the simulations.

### Automations

`sdk/automation` (Go module `github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk`, in the `VaultDock` repository) defines the document format, the catalogue of triggers/conditions/actions, validation (schema, graph, cycles, contexts, template variables, conflicting actions), template rendering and evaluation. The server validates and stores documents and runs dry runs; the desktop app re-validates them and evaluates them on file events. Evaluation is pure: actions produce a plan (upload/skip, destination, name, tags, folders to create, notifications, other sync tasks) that the engine applies. Loop protection: DAG-only graphs, step limits, chain depth ≤3 for start-task actions, a one-minute cooldown per triggered task, and event de-duplication.

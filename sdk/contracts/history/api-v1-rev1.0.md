<!-- Historical text of contract revision 1.0, restored by INT on 2026-10-18 from the planning session. Superseded by rev 1.1, which references it for fields marked "as rev 1.0". Read-only reference. -->

# Contract: VaultDock Backup & Recovery HTTP API v1 (LOCKED)

Status: **LOCKED v1.0**, 2026-10-17. Owner: INT (master agent).
Sub-agents implement this contract exactly. They **must not** change names, paths, fields, status codes or semantics. If a sub-agent believes the contract is wrong or incomplete, it stops that part, documents the issue under "Contract issues" in its `SUBAGENT-*.md` report, and continues with everything else. Only INT amends this file, by appending to §12 (Amendments) and bumping the minor version.

Go wire types for everything in this file live in **`pkg/wire`** (owner PKG-CLIENT, created in Phase 0 task P0-08). The server (`internal/api`) and the client (`pkg/client`) both import `pkg/wire`, and neither may define its own copy of these types. Web TypeScript types are hand-written per feature from this file.

---

## 1. Conventions (apply to every new endpoint)

| Topic | Rule |
|---|---|
| Base path | `/api/v1` |
| Encoding | JSON, UTF-8, `snake_case` keys. Request decoding rejects unknown fields (existing behaviour); responses may gain fields in minor versions, and clients must ignore unknown response fields |
| Timestamps | `int64` Unix **milliseconds** (existing convention, e.g. `modified_at`) |
| IDs | Database rows: `int64`. Repositories: `repo_uuid` (UUIDv4 string) plus numeric `id`. Repository objects: `oid` = 64 lowercase hex chars (SHA-256 of stored bytes). Upload sessions keep their existing string ids |
| Sizes | `int64` bytes |
| Errors | Existing envelope `{"error":{"code":"…","message":"…","details":{…}}}`. Validation errors go in `details.fields: {"field": "message"}` |
| Auth labels | `dev` = VD1 device signature (unchanged scheme, `pkg/protocol`); `sess` = session cookie + `X-CSRF-Token` on writes; `admin` = `sess` + admin role. A route marked `dev, sess` accepts either |
| Signed body limit | 2 MiB (existing). Routes marked **streamed** are exempt and the handler verifies the body against `X-VD-Content-SHA256` itself |
| Lists | `GET …?cursor=<opaque>&limit=<1..500, default 100>` returns `{"items":[…],"next_cursor":"…"}`. `next_cursor` is `""` at the end. Optional `total` only where cheap |
| Bulk results | `{"ok_count":N,"failed":[{"id":…,"code":"…","message":"…"}]}` |
| Long operations | `202 {"operation_id":N}`, then `GET /operations/{id}` returns an Operation (§3) |
| Plan/execute | `POST …/plan` returns `{"plan":{…},"plan_token":"…","plan_digest":"<sha256 hex>","expires_at":ms}`. `POST …/execute {"plan_token":"…"}` executes exactly that plan. A stale or expired token returns `409 plan_stale` |
| Approvals | A destructive execute returns `202 {"approval_id":N,"operation_id":N|null}` when a gate applies. Execution starts only after approval (§8) |
| Secrets | Never returned. GET returns `{"has_secret":bool,"secret_updated_at":ms}`. Write: `"secret": "<value>"` sets, `"secret": null` clears, omitted keeps |
| Capacity | Object PUT reuses `503` + `Retry-After` + `X-VD-Capacity: saturated` (existing upload behaviour) |
| Idempotency | Object PUT is idempotent by `oid`. Snapshot commit is idempotent by `snapshot_oid`. Run creation accepts an optional `client_run_uuid` for idempotent retry |

### 1.1 Error codes (new, in addition to existing ones)

| HTTP | code | Meaning |
|---|---|---|
| 400 | `invalid_oid` | oid not 64 lowercase hex, or kind unknown |
| 409 | `repo_locked` | Exclusive lock held. `details: {holder, expires_at}` |
| 422 | `object_hash_mismatch` | Stored bytes' SHA-256 ≠ oid |
| 409 | `pack_missing` | Snapshot references packs not present. `details: {missing:[oid…]}` (max 1000 listed) |
| 403 | `approval_required` | Action needs an approval and none was supplied/approved |
| 409 | `protected_snapshot` | Snapshot is protected |
| 413 | `backup_quota_exceeded` | User backup quota exceeded |
| 409 | `plan_stale` | plan_token unknown, expired, or the world changed |
| 409 | `conflict` | Sync upload `base_sha256` ≠ current server hash. `details: {current_sha256, current_modified_at}` |
| 403 | `device_cannot_delete` | Device lacks `can_delete` |
| 409 | `task_not_claimable` | Device task already claimed/finished |
| 404 | `repo_not_found` / `snapshot_not_found` / `object_not_found` | Specific not-found variants |

### 1.2 Streamed routes (whitelisted in SRV-0's route table)

- `PUT /api/v1/uploads/{id}/chunks/{n}` (existing)
- `PUT /api/v1/repos/{id}/objects/{kind}/{oid}`
- `POST /api/v1/repos/{id}/snapshots` (commit, max 64 MiB)
- `PUT /api/v1/repos/{id}/snapshots/{sid}/listing` (managed-metadata listing, max 256 MiB)
- `PUT /api/v1/device/inventory` (max 16 MiB)

For streamed routes the handler must hash the whole body while reading it and reject a mismatch with `400 body_mismatch` **before** committing any state.

### 1.3 Feature flags

`GET /api/v1/info` and `GET /api/v1/device/me` gain `"features": [..]`, with values: `backup.v1`, `device_tasks.v1`, `approvals.v1`, `versions.v1`, `sync_conflicts.v1`, `automation.v2`, `destinations.v1`, `ai.v1`. A server only advertises a flag once the feature is implemented. Clients must feature-detect.

---

## 2. Capabilities

`GET /backup/capabilities` (dev, sess) returns:

```json
{"formats":["vaultdock.repo/1"],"max_object_bytes":67108864,"pack_target_bytes":16777216,
 "max_missing_batch":20000,"max_commit_bytes":67108864,"server_destination":true,
 "encryption_modes":["e2e","managed_metadata"],"long_poll_seconds":25}
```

---

## 3. Summary and operations (SRV-0)

`GET /summary` (sess):
```json
{"pending_devices":0,"pending_approvals":0,"running_jobs":0,"failed_runs_24h":0,
 "unread_notifications":0,"open_conflicts":0,"readiness":"ok|warning|critical|unknown"}
```

`GET /operations/{id}` (sess, or dev if the device created it). Operation:
```json
{"id":1,"kind":"prune|gc|verify|replicate|catalog_rebuild|restore","state":"queued|running|succeeded|failed|cancelled",
 "progress":{"done":0,"total":0,"bytes":0},"result":{},"error":"","created_at":0,"updated_at":0}
```

---

## 4. Destinations (SRV-1, Phase 4; table created in Phase 0)

Destination:
```json
{"id":1,"scope":"user|global","name":"NAS","kind":"server|local|s3",
 "config":{"path":"","endpoint":"","bucket":"","prefix":"","region":"","provider":"aws|r2|b2|minio|other","path_style":false},
 "credential_scope":"client_only|server_held","has_secret":true,"secret_updated_at":0,
 "enabled":true,"health":"ok|degraded|unreachable|unknown","last_checked_at":0,
 "capabilities":{"object_lock":false,"conditional_put":true,"server_side_copy":false},
 "created_at":0,"updated_at":0}
```
SMB/NAS = `kind:"local"` with a host- or OS-mounted path (UNC paths allowed for client-only destinations).

| Method | Path | Auth |
|---|---|---|
| GET | `/destinations` | sess (dev: own enabled ones, without secrets) |
| POST | `/destinations` (body: Destination writable fields + `secret`) | sess |
| PATCH / DELETE | `/destinations/{id}` | sess (DELETE refused with `409 in_use` if repos reference it) |
| POST | `/destinations/test` (unsaved body) / `/destinations/{id}/test` | sess → `{"ok":bool,"checks":[{"name","ok","message"}]}` |
| GET | `/destinations/providers` | sess → form schema `[{kind,provider,fields:[{name,label,type,required,secret}]}]` |
| GET/POST/PATCH/DELETE | `/admin/destinations[/{id}]` | admin (scope `global`) |

---

## 5. Repositories, objects, locks, snapshots (SRV-2)

Repository:
```json
{"id":1,"repo_uuid":"…","name":"PC backups","destination_id":1,"format_version":1,
 "encryption_mode":"e2e|managed_metadata","created_by_device_id":3,"created_at":0,
 "usage":{"objects":0,"stored_bytes":0,"logical_bytes":0,"snapshots":0},"last_catalog_sync_at":0}
```

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/repos` | dev, sess | Body `{repo_uuid,name,destination_id,encryption_mode,format_version}`. Idempotent by `repo_uuid` (returns the existing one) |
| GET | `/repos`, `/repos/{id}` | dev, sess | |
| HEAD | `/repos/{id}/objects/{kind}/{oid}` | dev | 200 / 404. `Content-Length` = size |
| PUT | `/repos/{id}/objects/{kind}/{oid}` | dev, **streamed** | Body = object bytes. `X-VD-Content-SHA256` must equal `oid` for every kind (§5.1). 201 created, 200 already existed (idempotent), 422 `object_hash_mismatch` |
| GET | `/repos/{id}/objects/{kind}/{oid}` | dev, sess | HTTP Range supported |
| POST | `/repos/{id}/objects/missing` | dev | `{"kind":"data","oids":[…≤20000]}` → `{"missing":[…]}` |
| GET | `/repos/{id}/objects?kind=&cursor=&limit=` | dev | Lists oids + size (catalog rebuild / verify) |
| POST | `/repos/{id}/locks` | dev | `{"kind":"shared|exclusive","ttl_seconds":300}` → `{"lock_id":"…","expires_at":ms}`. `409 repo_locked` if incompatible |
| PUT | `/repos/{id}/locks/{lock_id}` | dev | Refresh (extends TTL) |
| DELETE | `/repos/{id}/locks/{lock_id}` | dev | Release |
| POST | `/repos/{id}/snapshots` | dev, **streamed** | Commit (§5.2) |
| PUT | `/repos/{id}/snapshots/{sid}/listing` | dev, **streamed** | Managed-metadata listing (§5.3). 409 if repo is `e2e` |

### 5.1 Object kinds

`kind` ∈ `config | keys | index | snapshots | data | locks`, matching the repository layout in [backup-format-v1.md](backup-format-v1.md) §2. For every kind, `oid = SHA-256(stored bytes)`, **except `config`**, whose oid is the fixed string `"0000000000000000000000000000000000000000000000000000000000000000"`. The server verifies the body hash against the `X-VD-Content-SHA256` header instead of the oid, and replacing it requires an exclusive lock.

### 5.2 Snapshot commit

Request:
```json
{"snapshot_oid":"…","job_id":12,"run_id":345,"parent_snapshot_id":null,
 "kind":"folder|profile|app|game|devproject|registry|inventory",
 "mode":"full|data_only|reinstall_inventory",
 "hostname":"DESKTOP-ABC","os_user":"ali","user_sid":"S-1-5-21-…","profile_path":"C:\\Users\\ali",
 "client_time":0,"tags":["daily"],
 "stats":{"file_count":0,"dir_count":0,"logical_bytes":0,"new_bytes":0,"new_objects":0,"duration_ms":0},
 "index_oids":["…"],"pack_oids":["…"]}
```
- `pack_oids` lists **every** data and tree pack the snapshot needs, including ones uploaded earlier. The GC reachability input relies on this.
- The server verifies that every `pack_oids`, `index_oids` and `snapshot_oid` object exists (`409 pack_missing` otherwise), inserts `snapshots` + `snapshot_packs` in batched transactions, and sets `job_runs.snapshot_id`.
- Response `201 {"snapshot_id":N}`. A repeated commit of the same `snapshot_oid` returns `200` with the same id.

### 5.3 Managed-metadata listing

Body: zstd-compressed **JSON Lines** (`Content-Encoding` is not used; the body is the raw zstd stream). One line per entry:
`{"path":"{DOCUMENTS}/a/b.txt","type":"file|dir|symlink","size":0,"mtime":ms,"attrs":0}`.
Paths use the path tokens from backup-format-v1.md §6. The server ingests the listing into `snapshot_files` and never stores it in the repository.

### 5.4 Snapshot catalog (sess)

Snapshot:
```json
{"id":1,"repo_id":1,"snapshot_oid":"…","job_id":12,"device_id":3,"parent_id":null,"kind":"folder","mode":"full",
 "hostname":"…","os_user":"…","profile_path":"…","client_time":0,"committed_at":0,
 "stats":{…},"tags":[],"protected":false,"protected_until":null,"status":"committed|forgotten|missing",
 "browsable":true}
```
`browsable` is true only when a managed-metadata listing exists.

| Method | Path | Notes |
|---|---|---|
| GET | `/snapshots?repo=&job=&device=&kind=&tag=&from=&to=&protected=&cursor=&limit=` | |
| GET | `/snapshots/{id}` | |
| GET | `/snapshots/{id}/tree?path=&cursor=&limit=` | Managed-metadata only; else `409 not_browsable` |
| GET | `/snapshots/{id}/search?q=&type=&min_size=&max_size=&cursor=` | Managed-metadata only |
| GET | `/snapshots/diff?a=&b=` | `{added,removed,changed}` counts + first 1000 paths (managed), or a pack delta (e2e) |
| PATCH | `/snapshots/{id}` | `{tags?, protected?, protected_until?}`. Setting `protected:false` on a protected snapshot → `202 {approval_id}` (§8) |
| POST | `/catalog/rebuild` `{repo_id}` | sess (owner) or admin → `202 {operation_id}` |

---

## 6. Jobs, runs, issues, device tasks, inventory (SRV-3)

BackupJob (desired state; devices pull):
```json
{"id":12,"device_id":3,"repo_id":1,"name":"Documents","kind":"folder|profile|app|game|devproject|registry|inventory",
 "mode":"full|data_only|reinstall_inventory",
 "spec":{"paths":["{DOCUMENTS}"],"exclude":["**/node_modules"],"ruleset_id":null,"source_ref":"","use_vss":"auto|required|off"},
 "schedule":{"type":"cron|daily|weekly|interval|manual","expr":"","at":"02:00","weekdays":[1,2,3,4,5],"every_minutes":0,"timezone":"Local","catch_up":"run_once|skip","jitter_minutes":10},
 "retention_policy_id":null,"priority":0,"enabled":true,"revision":7,"created_at":0,"updated_at":0}
```
`revision` increments on every change. Devices send `If-Match: <revision>` on PATCH to avoid lost updates (`409 conflict` on mismatch).

| Method | Path | Auth |
|---|---|---|
| GET | `/backup/jobs?device=&cursor=` | sess; dev (own device only) |
| POST / PATCH / DELETE | `/backup/jobs[/{id}]` | sess; dev may POST/PATCH jobs for **its own device** (desktop-created jobs) |
| POST | `/backup/jobs/{id}/runs` | dev → `{"run_id":N}`. Body `{"client_run_uuid":"…","trigger":"schedule|manual|automation|retry_skipped|task"}` |
| PATCH | `/backup/runs/{id}` | dev. Heartbeat ≤ 1 per 2 s: `{status, phase, files_total, files_done, bytes_total, bytes_done, bytes_new, bytes_uploaded, queue_packs, warnings, errors, eta_ms}` |
| POST | `/backup/runs/{id}/issues` | dev. `{"issues":[Issue…≤500]}` |
| GET | `/backup/runs?job=&device=&status=&cursor=`, `/backup/runs/{id}`, `/backup/runs/{id}/issues?class=&resolution=&cursor=` | sess |
| POST | `/backup/runs/{id}/issues/skip` | sess `{"ids":[…]}` or `{"class":"locked"}` → BulkResult |
| POST | `/backup/runs/{id}/retry-skipped` | sess → `{"task_id":N}` (creates a `retry_skipped` device task) |

Run `status` ∈ `queued|scanning|uploading|committing|completed|partial|failed|cancelled`. `partial` = committed with skipped items.

Issue:
```json
{"id":1,"run_id":345,"path":"{APPDATA}/x.db",
 "class":"locked|access_denied|not_found|path_too_long|io|network|server|crypto|cloud_placeholder|vss|other",
 "severity":"warning|skipped|error","detail":"…","first_seen":0,"last_seen":0,"count":1,
 "retryable":true,"resolution":"open|skipped|skipped_all|retried|resolved","retry_run_id":null}
```

DeviceTask:
```json
{"id":1,"device_id":3,"kind":"backup_now|restore|retry_skipped|verify|prune_compact|test_restore|automation_op",
 "payload":{},"status":"pending|claimed|done|failed|cancelled","approval_id":null,
 "created_by":"user|automation|ai|server","created_at":0,"claimed_at":0,"finished_at":0,"result":{}}
```

| Method | Path | Auth |
|---|---|---|
| GET | `/device/tasks?wait=0..25` | dev. Long-poll: returns `{"items":[DeviceTask…]}` as soon as ≥1 is claimable, else empty after `wait` seconds. **Returning a task claims it** (status → `claimed`). Tasks whose `approval_id` is not approved are never returned |
| POST | `/device/tasks/{id}/result` | dev `{"status":"done|failed","result":{}}` |
| POST | `/device/tasks` | sess `{"device_id","kind","payload"}`. Destructive kinds (`restore`, `prune_compact`) → `202 {"task_id","approval_id"}` |
| GET | `/device/tasks/list?device=&status=&cursor=` | sess |

Payloads by kind:
- `backup_now {job_id}`
- `retry_skipped {run_id, issue_ids?}`
- `verify {repo_id, snapshot_id?, depth:"metadata|sample|full", sample_pct?}`
- `test_restore {snapshot_id, sample_files:int}`
- `prune_compact {repo_id, prune_plan_id}`
- `restore {restore_id}`
- `automation_op {automation_id, node_id, op:{kind,params}}`

**Automation runtime decision (locked):** device-runtime operations queued by the server are delivered as `automation_op` device tasks. There are no separate `/device/automation-requests` endpoints.

Inventory:
- `PUT /device/inventory` (dev, streamed, opt-in): body is the plaintext JSON inventory from backup-format-v1.md §8.
- `GET /devices/{id}/apps?cursor=` (sess) returns `[{name,version,publisher,source,package_id,match_method,tier,install_scope,mode,data_bytes,last_snapshot_id,flags:["not_portable","settings_before_install"]}]`.

---

## 7. Restore (SRV-3; planner = `pkg/restore`)

| Method | Path | Auth |
|---|---|---|
| POST | `/restore/plan` | sess. Body `{snapshot_id, selection:[{path,include:bool}], target_device_id, target_user:{sid?,profile_path?}, mapping:{auto:true, overrides:[{from,to}]}, conflict_policy:"skip|overwrite|rename|newer", reinstall_apps:[package refs], include_registry:bool}` → plan response (§1) where `plan = {summary:{files,bytes,conflicts,reinstalls}, mappings:[{token,from,to,auto:bool}], actions:[…first 1000], warnings:[{code,message}], not_portable:[…]}` |
| POST | `/restore/execute` | sess `{"plan_token"}` → `202 {"restore_id","approval_id"}`. After approval the server creates a `restore` device task |
| GET | `/restore/{id}`, `/restore/{id}/items?status=&cursor=` | sess |

Restore `status` ∈ `awaiting_approval|queued|running|completed|partial|failed|cancelled`.
For E2E repos the server cannot compute file-level conflicts. The plan then contains snapshot-level mapping only, and the device computes the final per-file actions and reports them as items.

---

## 8. Approvals (SRV-3; used by everyone)

Approval:
```json
{"id":1,"kind":"restore|prune|gc|unprotect_snapshot|device_task|automation_op|ai_proposal",
 "subject_type":"restore|prune_plan|snapshot|device_task|automation","subject_id":1,
 "plan_digest":"<sha256 hex of canonical plan JSON>","summary":"Delete 14 snapshots (38.2 GB)",
 "risk":"low|medium|high","requested_by":"user|automation|ai|device","requester_ref":"",
 "state":"pending|approved|rejected|expired|consumed","expires_at":0,
 "decided_by":null,"decided_at":null,"reason":""}
```

| Method | Path | Auth |
|---|---|---|
| GET | `/approvals?state=&cursor=`, `/approvals/{id}` | sess |
| POST | `/approvals/{id}/approve` | sess + CSRF. Body `{"confirm_text":"…"}`, required when `risk:"high"` (must equal the subject name shown in `summary`) |
| POST | `/approvals/{id}/reject` | sess `{"reason":""}` |

Rules:
1. Approvals are created only by the server.
2. Only a session user who owns the subject (or an admin, for global subjects) can decide.
3. Devices, automations and AI can never approve.
4. Approving executes the bound plan only if `plan_digest` still matches; otherwise the state becomes `expired` and the requester must re-plan.
5. Default expiry is 72 h.
6. `consumed` = executed.

---

## 9. Retention, prune, GC, verify, replication, analytics (SRV-4 / SRV-6)

RetentionPolicy rules: `{"keep_last":0,"keep_hourly":0,"keep_daily":7,"keep_weekly":4,"keep_monthly":12,"keep_yearly":0,"keep_within_days":0,"keep_tags":[]}`. Protected snapshots are always kept. A policy whose rules are all zero is invalid (`400`).

| Method | Path | Auth |
|---|---|---|
| GET/POST/PATCH/DELETE | `/retention-policies[/{id}]` | sess |
| POST | `/repos/{id}/prune/plan` `{policy_id}` | sess → plan response; `plan = {forget:[{snapshot_id,reason}], keep:[{snapshot_id,reasons:[]}], packs_to_delete:N, bytes_reclaimed:N, bytes_reclaimable_after_compaction:N}` |
| POST | `/repos/{id}/prune/execute` `{plan_token}` | sess → `202 {approval_id, operation_id}` |
| POST | `/admin/gc/plan`, `/admin/gc/execute` | admin; same plan/approval pattern (orphan objects across repos) |
| GET/PUT | `/admin/backup-settings` | admin `{grace_hours:24, max_object_mb:64, scrub_cron:"", db_backup_cron:"", default_retention_policy_id:null}` |
| POST | `/repos/{id}/verify` `{depth:"hash|sample|full", sample_pct}` | sess → `202 {operation_id}` (hash = server scrub; sample/full = device task) |
| GET/POST/PATCH/DELETE | `/replication[/{id}]` | sess `{src_repo_id, dst_destination_id, mode:"mirror|copy_new", schedule, enabled}` + status `{lag_objects, lag_bytes, last_run_at, state}` |
| GET | `/repos/{id}/analytics`, `/storage/analytics?group=job|app|device|destination&range=30d` | sess → `{logical_bytes, stored_bytes, dedup_ratio, reclaimable_bytes, series:[{t,logical,stored}], top:[{key,unique_bytes}]}` |

---

## 10. Sync additions (SRV-5)

- `POST /uploads` gains optional `"base_sha256"`. When it is present and the current file's sha256 differs, the server returns `409 conflict` (`details.current_sha256`) **unless** `on_conflict` is `keep_both`, in which case the server stores the upload as `name (conflict <device> <YYYY-MM-DD HHmmss>).ext` and returns the session with `"conflict":true`.
- Versions: `GET /files/{id}/versions?cursor=` returns `[{id,sha256,size,modified_at,origin_device_id,created_at,pinned}]`. Also `POST /files/{id}/versions/{vid}/restore`, `PUT /files/{id}/versions/{vid}/pin {pinned}`, and `GET /files/{id}/versions/{vid}/content` (Range). Session only; devices are read-only.
- `GET/POST/PATCH/DELETE /sync-folders[/{id}]` (sess; dev GET own): `{id,device_id,local_path,remote_path,ruleset:{…},schedule:{…},conflict_policy:"keep_both|local_wins|skip",enabled,revision,acked_revision}`.
- `GET /sync/conflicts?state=&cursor=` and `POST /sync/conflicts/resolve {"ids":[…],"choice":"keep_local|keep_remote|keep_both"}` → BulkResult.
- Zip download: missing blobs are written as a `MISSING.txt` entry listing the affected paths, and the response trailer `X-VD-Missing: <count>` is set. The web shows a warning when the count is > 0.

---

## 11. Readiness, notifications, AI (SRV-7)

Readiness:
- `GET /readiness?device=` returns `{"score":0..100,"status":"ok|warning|critical|unknown","devices":[{device_id,checks:[{id,status,message,fix_link}],jobs:[{job_id,last_success_at,open_issues,vss_used,verified_at,test_restored_at}]}]}`.
- Check ids: `recent_backup`, `open_issues`, `vss_available`, `verified_recently`, `test_restore_recent`, `recovery_key_confirmed`, `service_installed`, `redundant_destination`, `not_portable_items`.
- The device supplies `recovery_key_confirmed`, `service_installed` and `vss_available` via `PUT /device/backup-state {…}` (dev).

Notifications:
- `GET/PUT /account/notification-prefs` with `[{event,channel:"in_app|desktop|email|webhook",enabled}]`.
- Events: `backup.failed`, `backup.partial`, `readiness.degraded`, `quota.warning`, `replication.lagging`, `approval.pending`, `verify.failed`.

AI:
- `GET/PUT /account/ai` with `{provider:"anthropic|openai|openai_compatible",model,endpoint,has_secret,scopes:["diagnostics","recommendations"],enabled}` (`secret` write-only).
- `POST /ai/diagnose {run_id?|device_id?, question?}` returns `{report_markdown, proposals:[{kind,summary,approval_id?}]}`. Proposals that would change anything are created as approvals with `requested_by:"ai"`; nothing is executed directly.
- Every call is written to `ai_audit`.

---

## 12. Amendments

_None yet. INT appends dated, versioned entries here._

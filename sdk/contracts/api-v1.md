# Contract: VaultDock Backup & Recovery HTTP API v1 (LOCKED)

Status: **LOCKED, contract revision 1.1**, 2026-10-18. Supersedes revision 1.0 (2026-10-17, never implemented; see §14). Owner: INT (master agent).

Sub-agents implement this contract exactly and never change names, paths, fields, status codes or semantics. Contract problems go under "Contract issues" in the task's `SUBAGENT-*.md`. Only INT amends this file (§14).

Go wire types for everything here live in **`VaultDock/sdk/wire`**, the shared SDK module consumed by `vad-server` and the Windows client (owner SDK-WIRE). Neither side defines its own copies. Web TypeScript types (in `vad-server/web`) are hand-written from this file.

---

## 1. Conventions

| Topic | Rule |
|---|---|
| Base path | `/api/v1` over **HTTPS**, exposed directly, behind a reverse proxy, or through Cloudflare Tunnel. Plain HTTP is accepted by the Windows client only for `localhost` |
| Encoding | JSON, UTF-8, `snake_case`. Requests reject unknown fields; clients ignore unknown response fields |
| Timestamps | `int64` Unix milliseconds |
| IDs | Rows: `int64`. Repositories: `repo_uuid` + `id`. Objects: `oid` (64 lowercase hex). Windows profiles: `sid` string. Modules: string ids (§6A) |
| Errors | `{"error":{"code","message","details"}}`. Validation errors go in `details.fields` |
| Auth labels | `dev` = VD1 device signature; `sess` = session + CSRF on writes; `admin` = admin session; **`super`** = session of a user with `is_superuser` (§11B) |
| Signed body limit | 2 MiB unless the route is **streamed** (§1.2) |
| Lists | `?cursor=&limit=` (≤500, default 100) returns `{"items":[…],"next_cursor":""}` |
| Bulk | `{"ok_count":N,"failed":[{"id","code","message"}]}` |
| Long operations | `202 {"operation_id"}`, then `GET /operations/{id}` |
| Plan/execute | `…/plan` returns `{plan, plan_token, plan_digest, expires_at}`, then `…/execute {plan_token}`. Stale tokens get `409 plan_stale` |
| Approvals | A gated destructive action returns `202 {"approval_id", "operation_id"|null}` |
| Secrets | Never returned. GET shows `{"has_secret","secret_updated_at"}`. `secret: "v"` sets, `null` clears, omitted keeps |
| Capacity | `503` + `Retry-After` + `X-VD-Capacity: saturated` |
| Idempotency | Object PUT by oid. Snapshot commit by `snapshot_oid`. Runs by `client_run_uuid`. Sync uploads by `(folder_id, path, base_revision, sha256)` |
| Module gating | A request that needs a device module the device has not enabled returns `409 module_unavailable` with `details.module` (§6A) |

### 1.1 Error codes (new)

| HTTP | code | Meaning |
|---|---|---|
| 400 | `invalid_oid` | Bad oid or unknown kind |
| 409 | `repo_locked` | `details {holder, expires_at}` |
| 422 | `object_hash_mismatch` | Body hash ≠ oid |
| 409 | `pack_missing` | `details.missing[≤1000]` |
| 403 | `approval_required` | |
| 409 | `protected_snapshot` | |
| 413 | `backup_quota_exceeded` | |
| 409 | `plan_stale` | |
| 409 | `conflict` | Sync precondition failed. `details {current_revision, current_sha256, current_modified_at}` |
| 403 | `device_cannot_delete` | Outside a two-way folder the device is authorised for |
| 409 | `task_not_claimable` | |
| 404 | `repo_not_found` / `snapshot_not_found` / `object_not_found` / `profile_not_found` | |
| 409 | `module_unavailable` | `details {module, device_id}` |
| 403 | `profile_not_authorised` | The profile has no active consent |
| 423 | `profile_blacklisted` | Cleared only by a superuser + re-consent |
| 403 | `not_superuser` | |
| 409 | `escrow_unavailable` | Operation needs full escrow (or the server KEK is missing) |
| 409 | `sik_mismatch` | The device's pinned server identity ≠ the server's |
| 409 | `mass_delete_guard` | Two-way delete batch exceeds the folder threshold; requires confirmation |
| 422 | `endpoint_not_allowed` | AI endpoint blocked by SSRF policy (§11) |
| 409 | `env_change_requires_approval` | |

### 1.2 Streamed routes

- `PUT /uploads/{id}/chunks/{n}` (existing)
- `PUT /repos/{id}/objects/{kind}/{oid}`
- `POST /repos/{id}/snapshots`
- `PUT /device/inventory`

The handler hashes the whole body against `X-VD-Content-SHA256` before committing any state.

### 1.3 Feature flags

`/info` and `/device/me` carry `"features":[…]`:
`backup.v1`, `escrow.v1`, `device_tasks.v1`, `approvals.v1`, `versions.v1`, `sync_two_way.v1`, `profiles.v1`, `modules.v1`, `destinations.v1`, `ai.v1`, `deploy_env.v1`. A flag is advertised only when the feature is implemented.

---

## 2. Capabilities

`GET /backup/capabilities` (dev, sess):
```json
{"formats":["vaultdock.repo/1"],"max_object_bytes":67108864,"pack_target_bytes":16777216,
 "max_missing_batch":20000,"max_commit_bytes":67108864,"escrow_modes":["full","metadata"],
 "default_escrow_mode":"full","long_poll_seconds":25,"modules_supported":["core","sync","automation","vss","discovery","reinstall","profiles"]}
```

---

## 3. Summary and operations

- `GET /summary` (sess) returns `{pending_devices, pending_approvals, running_jobs, failed_runs_24h, unread_notifications, open_conflicts, blacklisted_profiles, readiness}`.
- `GET /operations/{id}` (sess; or dev for operations it created) returns `{id, kind, state, progress{done,total,bytes}, result, error, created_at, updated_at}`.
- Operation `kind` ∈ `prune|gc|verify|replicate|catalog_rebuild|restore|rotate_keys|snapshot_download`.

---

## 4. Destinations (server-side only)

Devices never receive destination credentials. **Direct-to-cloud backup is out of scope** (owner decision 9).

Destination:
```json
{"id","scope":"user|global","name","kind":"server|local|s3",
 "config":{"path","endpoint","bucket","prefix","region","provider":"aws|r2|b2|minio|other","path_style"},
 "has_secret","secret_updated_at","enabled","health":"ok|degraded|unreachable|unknown","last_checked_at",
 "capabilities":{"object_lock","conditional_put","server_side_copy"},"created_at","updated_at"}
```
`local` covers server disks and NAS/SMB shares mounted on the Docker host.

| Method | Path | Auth |
|---|---|---|
| GET | `/destinations` | sess (dev: names and ids only) |
| POST, PATCH, DELETE | `/destinations[/{id}]` | sess (`409 in_use` on DELETE) |
| POST | `/destinations/test`, `/destinations/{id}/test` | sess |
| GET | `/destinations/providers` | sess |
| CRUD | `/admin/destinations[/{id}]` | admin |

---

## 5A. Server identity, enrolment key exchange, rotation

- `POST /device/enroll` (existing; new **required** fields for new clients) adds: `box_pub` (b64 X25519), `install_mode` (`simple|advanced`), `core_version`.
- `GET /device/enroll/{id}` (existing poll), after approval, also returns:
  ```json
  {"server_identity":{"sik_pub","sik_fingerprint","sek_pub","sek_version","sek_sig"}}
  ```
  The device pins `sik_fingerprint`. The web approval page shows the same fingerprint next to the verification code.
- `GET /keys/server` (dev, sess) returns the current `server_identity`. A device whose pinned SIK differs gets `409 sik_mismatch` on any key operation and stops backup until the user re-pins through the UI with an admin's confirmation.
- Key sharing to a newly approved device is delivered as device task `key_share` with payload `{repo_id, key_file_oid}`. The server has already written the `device` wrap under full escrow. Under metadata escrow, the task goes to an **existing** device with payload `{repo_id, target_device_id, target_box_pub}` asking it to write the wrap.
- `POST /repos/{id}/keys/rotate` (sess; owner) → `202 {operation_id}`. Device revocation triggers a rotation automatically.
- `PATCH /repos/{id}` `{"escrow_mode":"full|metadata"}` (sess; owner) → `202 {approval_id}`. Approval `kind:"escrow_change"`, risk high; the change rotates the epoch.
- `GET /repos/{id}/keys/status` (sess, dev) returns `{current_epoch, recipients:[{kind,label,epoch,device_id?}], recovery_wrapped_current:bool, escrow_mode}`.

---

## 5. Repositories, objects, locks, snapshots

Repository:
```json
{"id","repo_uuid","name","destination_id","format_version":1,"escrow_mode":"full|metadata",
 "current_epoch","created_by_device_id","created_at",
 "usage":{"objects","stored_bytes","logical_bytes","snapshots"},"last_catalog_sync_at"}
```

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/repos` | dev | `{repo_uuid,name,destination_id,escrow_mode="full",format_version:1}`. Idempotent by uuid. One repo per (user, destination) |
| GET | `/repos`, `/repos/{id}` | dev, sess | |
| HEAD, PUT, GET | `/repos/{id}/objects/{kind}/{oid}` | dev (GET also sess) | PUT streamed; `config` uses the fixed oid `0`×64 and needs an exclusive lock. GET supports Range |
| GET | `/repos/{id}/objects?kind=&cursor=` | dev | |
| POST | `/repos/{id}/objects/missing` | dev | `{kind, oids[≤20000]}` → `{missing}`. Pending-delete objects count as missing |
| POST / PUT / DELETE | `/repos/{id}/locks[/{lock_id}]` | dev | `{kind, ttl_seconds}` |
| POST | `/repos/{id}/snapshots` | dev, streamed | Commit (§5.2) |

Kinds: `config|keys|chain|index|snapshots|data|locks`.

### 5.2 Snapshot commit

The body has the same fields as rev 1.0 (`snapshot_oid, job_id, run_id, parent_snapshot_id, kind, mode, hostname, os_user, user_sid, profile_path, client_time, tags, stats, index_oids, pack_oids`) plus:
- `epoch`
- `profile_consent_id`

The server:
1. verifies that the objects exist and that `profile_consent_id` is active for `(device, user_sid)` (`403 profile_not_authorised` / `423 profile_blacklisted`);
2. inserts the catalog rows;
3. queues **catalog ingestion**: it decrypts trees with `K_meta` from escrow and fills `snapshot_files` (encrypted at rest; §5.4).

Response: `201 {"snapshot_id"}` (`200` on replay).

### 5.4 Snapshot catalog (sess)

Snapshot = rev 1.0 fields + `epoch`, `profile{sid,username}`, `ingest_state:"pending|done|failed"`. `browsable` is true when `ingest_state = done`.

| Method | Path | Notes |
|---|---|---|
| GET | `/snapshots?repo=&job=&device=&profile=&kind=&tag=&from=&to=&protected=&cursor=` | |
| GET | `/snapshots/{id}` | |
| GET | `/snapshots/{id}/tree?path=&cursor=` | |
| GET | `/snapshots/{id}/search?q=&type=&min_size=&max_size=&cursor=` | `q` matches whole name tokens and prefixes (blind-indexed; **no arbitrary substring**) |
| GET | `/snapshots/diff?a=&b=` | |
| PATCH | `/snapshots/{id}` | tags/protect; unprotect → approval |
| GET | `/snapshots/{id}/content?path=` | **full escrow only** (else `409 escrow_unavailable`). Range; same CSP and download rules as `/files/{id}/content` |
| POST | `/snapshots/{id}/download` | `{paths[]}` → `202 {operation_id}`. Result `{download_url}`, a single-use tokenised zip valid 1 h. Full escrow only |
| POST | `/catalog/rebuild` | `{repo_id}` → operation |

The server stores catalog paths and names **encrypted at rest** (per-user catalog DEK wrapped by the server KEK), with an HMAC blind index for search.

---

## 6. Jobs, runs, issues, device tasks, inventory

BackupJob = rev 1.0 fields + `profile_sid` (the authorised Windows profile the job reads) + `required_modules` (computed by the server: e.g. `kind:"app"` → `["discovery"]`; `use_vss:"required"` → `["vss"]`) + `state:"active|suspended_by_mode|suspended_by_profile"`.
- Creating or enabling a job whose `required_modules` the target device lacks → `409 module_unavailable`.
- A device downgrading its mode moves affected jobs to `suspended_by_mode`. **They are never deleted.**

Endpoints are as rev 1.0:
- `/backup/jobs`, `/backup/jobs/{id}/runs`, `PATCH /backup/runs/{id}`, `/backup/runs/{id}/issues`, `…/issues/skip`, `…/retry-skipped`;
- `/device/tasks?wait=`, `/device/tasks/{id}/result`, `POST /device/tasks`, `/device/tasks/list`.

Issue classes add: `profile_unauthorised`, `module_unavailable`, `guard_blocked` (system-file protection, §10.3).

DeviceTask `kind` ∈ `backup_now | restore | retry_skipped | verify | prune_compact | test_restore | automation_op | key_share | rewrap | profile_consent_request | mode_change_request`.

New payloads:
- `key_share` (§5A);
- `rewrap {repo_id, epoch}`;
- `profile_consent_request {request_id, sids[]|"all", requested_by}`;
- `mode_change_request {target_mode, requested_by}`. Always requires local confirmation by a Windows administrator on the device; the server can never switch a device's mode by itself.

The server only delivers tasks whose `required_modules` are enabled on the device. Otherwise the task fails immediately with `module_unavailable`.

Inventory: `PUT /device/inventory` (dev, streamed, opt-in, `discovery` module) and `GET /devices/{id}/apps` as rev 1.0.

---

## 6A. Install modes and modules (owner requirement 11)

Module ids (fixed for v1):

| id | Simple | Advanced | Delivery |
|---|---|---|---|
| `core` | ✔ | ✔ | Installer (agent service, tray/UI, folder backup, schedules, browse, file restore, keystore) |
| `sync` | – | ✔ | Built into core, enabled by mode (one-way + two-way sync) |
| `automation` | – | ✔ | Built into core, enabled by mode (schedules are core) |
| `profiles` | – (active user only) | ✔ | Built into core, enabled by mode (multi-profile protection) |
| `vss` | – | ✔ | Downloaded component |
| `discovery` | – | ✔ | Downloaded component (apps, games, dev projects, registry export) |
| `reinstall` | – | ✔ | Downloaded component (winget/choco/scoop) |

| Method | Path | Auth | Body / response |
|---|---|---|---|
| PUT | `/device/modules` | dev | `{"install_mode":"simple|advanced","core_version","modules":[{"id","version","state":"enabled|disabled|installing|failed","error"}]}` → `204`. Sent at enrolment, on change, and daily |
| GET | `/devices/{id}/modules` | sess (owner/admin) | Same shape + `updated_at` |
| GET | `/modules/catalog` | dev, sess | `[{"id","name","summary","modes":["advanced"],"delivery":"builtin|component","requires_features":["sync_two_way.v1"]}]` |
| POST | `/devices/{id}/mode-request` | sess (owner/admin) | `{"target_mode"}` → creates a `mode_change_request` task (local admin confirmation required) |

`GET /devices` gains `install_mode`, `modules_summary`. The server never assumes a module from the mode alone; it always uses the reported `modules`.

---

## 6B. Windows profiles, consent, blacklist (owner decision 6)

DeviceProfile:
```json
{"id","device_id","sid","username","profile_path","vaultdock_user_id",
 "state":"active_default|requested|authorised|blacklisted|cleared_pending_consent|revoked",
 "consent_id","consent_at","requested_by_user_id","request_scope":"selected|all",
 "blacklisted_at","blacklist_reason":"password_changed|password_reset|sid_changed|account_disabled|account_removed|profile_moved|consent_unverifiable",
 "cleared_by","cleared_at","updated_at"}
```

| Method | Path | Auth | Notes |
|---|---|---|---|
| PUT | `/device/profiles` | dev | Full state report from the agent, including consent records (`consent_id`, `sid`, `consent_at`, `proof_digest`, which is a SHA-256 of the DPAPI-bound consent blob and never the blob itself) |
| POST | `/device/profiles/{sid}/security-event` | dev | `{"signal":"password_changed|password_reset|sid_changed|account_disabled|account_removed|profile_moved|consent_unverifiable","observed_at","evidence":"local_password_age|dpapi_unprotect_failed|ldap_pwdlastset|eventlog_4723|eventlog_4724|sid_lookup"}`. Server sets `blacklisted`; jobs → `suspended_by_profile`; notifies superusers |
| GET | `/devices/{id}/profiles` | sess (device owner, admin) | |
| POST | `/devices/{id}/profiles/requests` | admin | `{"sids":[…]}` or `{"all":true}` → creates a `profile_consent_request` task. Each affected Windows user must still sign in, open VaultDock and consent |
| POST | `/admin/device-profiles/{id}/clear` | **super** | `{"confirm_text"}` → state `cleared_pending_consent`. **Never re-enables by itself**: the Windows user must consent again locally |

Rules:
1. The active user's profile is consented implicitly at first run by that user (state `active_default`, still a recorded consent).
2. The agent never reads or writes a profile whose state is not `active_default` or `authorised`.
3. Snapshots and sync for a blacklisted profile are rejected server-side (`423`).
4. Passwords are never transmitted or stored.

---

## 7. Restore

As rev 1.0, plus:
- `target_profile_sid` (must be `authorised` or `active_default` on the target device);
- `reinstall_apps` requires the `discovery` and `reinstall` modules on the target device. Otherwise the plan marks those items `data_only` with warning `module_unavailable`.

The plan's `guard` section lists items blocked by system-file protection (§10.3); they can never be overridden through the API.

---

## 8. Approvals

As rev 1.0. Kinds add `escrow_change` and `two_way_enable` (enabling two-way on an existing folder, risk medium).

---

## 9. Retention, prune, GC, verify, replication, analytics

As rev 1.0. Under full escrow, compaction runs server-side as an operation. Under metadata escrow it runs as a `prune_compact` device task.

---

## 10. Sync (owner decision 2: one-way default, two-way optional, per-folder)

### 10.1 Sync folders (desired state, device pulls)

```json
{"id","device_id","profile_sid","local_path","remote_path",
 "direction":"upload|two_way","conflict_policy":"keep_both|newer_wins|local_wins|remote_wins|ask",
 "delete_policy":"propagate_to_trash|ignore","mass_delete_threshold":{"files":50,"percent":10},
 "ruleset":{…},"schedule":{…},"enabled","revision","acked_revision","state":"active|paused|suspended_by_mode|suspended_by_profile",
 "created_at","updated_at"}
```
- `direction` defaults to **`upload`** (client → server).
- Changing an existing folder to `two_way` requires approval (`two_way_enable`) and an initial reconcile plan shown to the user.
- `two_way` requires the `sync` module (enabled in Advanced).
- CRUD: `/sync-folders[/{id}]` (sess; dev GET own + PATCH `acked_revision` / `state`).

### 10.2 Change feed and transfers

- `GET /sync/changes?folder_id=&since=<seq>&wait=0..25` (dev) returns:
  ```json
  {"items":[{"seq","node_id","op":"create|update|move|delete","path","old_path","revision","sha256","size","modified_at","device_id"}],"next_since","reset":false}
  ```
  `reset: true` means the tombstone window was exceeded and the device must do a full reconcile with `GET /files?path=`.
- Every `nodes` row carries `revision` (monotonic per node), and `sync_changes` holds `seq` per user. Tombstones are retained ≥ 90 days (admin setting).
- **Upload** reuses `POST /uploads` with `folder_id`, `base_revision` (`null` = create) and `base_sha256`. A mismatch returns `409 conflict` unless `on_conflict:"keep_both"`, in which case the server stores the upload as `name (conflict <device> <YYYY-MM-DD HHmmss>).ext` and returns `conflict:true`.
- **Download** reuses `GET /files/{id}/content` (dev; Range).
- **Delete/move from device:** `POST /sync/folders/{id}/ops {"ops":[{"op":"delete|move","path","new_path","base_revision"}],"confirm_mass_delete":false}` → BulkResult.
  - Deletes go to the trash and are never purged.
  - Only paths inside a `two_way` folder of this device are allowed (`403 device_cannot_delete` otherwise).
  - Exceeding the threshold without `confirm_mass_delete` returns `409 mass_delete_guard`. Confirmation must come from the local user through the UI.

### 10.3 System-file protection (normative for the client)

The client never syncs, restores or writes into protected locations:
- `%WINDIR%`, `%ProgramFiles%`, `%ProgramFiles(x86)%`;
- `%ProgramData%\Microsoft`;
- `System Volume Information`, `$Recycle.Bin`, the drive root of the system volume;
- page/hibernate files;
- other profiles without consent.

It also never follows reparse points when writing. The server rejects sync-folder definitions whose `local_path` normalises to one of these, using the token list in backup-format §6 (`422 validation`).

### 10.4 Versions and conflicts

- Versions: `GET /files/{id}/versions`, `POST …/versions/{vid}/restore`, `PUT …/pin`, `GET …/content` (sess; devices read-only).
- `GET /sync/conflicts?state=&cursor=`; `POST /sync/conflicts/resolve {ids, choice:"keep_local|keep_remote|keep_both"}` → BulkResult. `ask` policy conflicts appear here and in the client.
- Zip download: missing blobs are listed in `MISSING.txt` and counted in `X-VD-Missing` (rev 1.0).

---

## 11. Readiness, notifications, AI

- **Readiness** checks add `profile_authorised`, `install_mode` (informational) and `modules_healthy`. `PUT /device/backup-state` as rev 1.0.
- **Notifications** as rev 1.0. Events add `profile.blacklisted`, `module.failed`.
- **AI** (owner decision 8: generic OpenAI-compatible only):
  - `GET/PUT /account/ai` with `{"enabled","base_url","model","has_secret","secret_updated_at","scopes":["diagnostics","recommendations"]}`. `secret` is the API key, write-only.
  - The server calls `POST {base_url}/chat/completions` (OpenAI-compatible schema) with redacted context.
  - `base_url` must be HTTPS. Loopback, private and link-local addresses are rejected with `422 endpoint_not_allowed` unless an admin enables `ai_allow_private_endpoints` (needed for LAN-hosted models).
  - Redirects are not followed.
  - `POST /ai/diagnose` as rev 1.0. Proposals become approvals; nothing executes directly. All calls are audited.

---

## 11A. Deployment environment management (server deployment requirement)

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/admin/deploy/env` | super | `{"schema_version","added_by_upgrade":[{"name","default","since"}],"obsolete_present":[{"name","deprecated_since","replacement","note"}],"pending_removals":[…]}`. Values of secret variables are never returned |
| POST | `/admin/deploy/env/removals` | super | `{"names":[…],"confirm_text":"REMOVE <n> VARIABLES"}` → writes `${STATE}/deploy/env-removal-approvals.json` (name, approved_by, approved_at). `install.sh upgrade` applies **only** names listed there or confirmed interactively, then clears the file |
| DELETE | `/admin/deploy/env/removals/{name}` | super | Withdraw a pending approval |

The server never edits the host `.env`. Only `install.sh` does.

## 11B. Superuser

- `users.is_superuser`. The first administrator created at setup is a superuser. A superuser can grant or revoke `is_superuser` for other admins (`PATCH /admin/users/{id} {"is_superuser"}`, super). At least one superuser must always remain (`409`).
- Superuser-only: profile blacklist clearing, env removals, SEK/SIK rotation (`POST /admin/keys/rotate-server`, super → operation).

---

## 14. Amendments

- **Rev 1.1 (2026-10-18, INT), owner decisions 1, 2, 6, 8, 9, the deployment requirements, and requirement 11 (install modes).**
  - Removed: managed-metadata listing upload, `client_only` destination credentials, `encryption_mode`.
  - Added: §5A key exchange, `escrow_mode`, server-side browse and download under full escrow, encrypted catalog with blind-index search, two-way sync (§10), modules (§6A), profiles and consent (§6B), env management (§11A), superuser (§11B), generic OpenAI-compatible AI, and the new error codes and flags.
  - Rev 1.0 was never implemented.
- **Rev 1.1a (2026-10-18, INT), clarification only.** Wherever this file says "as rev 1.0", the normative text is [history/api-v1-rev1.0.md](history/api-v1-rev1.0.md) (restored verbatim), except where rev 1.1 changes or removes it. Shapes defined in neither revision are implementer choices recorded in the SUBAGENT reports and reconciled by INT.
  - Job schedule `weekdays` use ISO numbering: 1 = Monday … 7 = Sunday. `interval` schedules are anchored to the Unix epoch. `cron` is the standard 5-field form. Missed-run grace is 5 minutes (implemented in `sdk/schedule`).
  - Restore: `target_profile_sid` (rev 1.1) takes precedence over rev 1.0 `target_user.sid` when both are present.
- **Rev 1.1b (2026-10-18, INT), Phase 1 resolutions.** Additive only.
  - **Built-in server destination.** The server keeps one built-in destination row (`kind='server'`, `scope='global'`, localfs under `<default storage>/backup/`), created on demand. `POST /repos` with `destination_id` omitted or `0` uses it.
  - **Config replace.** A config PUT whose content differs from the stored config replaces it atomically and returns `201`; identical content returns `200`. The caller must hold an unexpired exclusive lock on the repo (checked against the calling device), else `409 repo_locked`.
  - **Object hash.** For every kind except `config`, a body whose SHA-256 differs from the oid is `422 object_hash_mismatch`; transport-level body/header mismatches stay `400 body_mismatch`.
  - **Locks.** Refreshing or releasing an unknown or expired lock returns `404 lock_not_found`.
  - **Objects.** There is no object DELETE route; garbage collection is server-side (§9).
  - **SIK fingerprint.** Uppercase hex of the first 16 bytes of `SHA-256(sik_pub)`, in 8 groups of 4 joined by `-`. Clients compare case-insensitively, ignoring `:`, `-` and spaces.
  - **`POST /admin/keys/rotate-server`** rotates the SEK only (operation kind `rotate_keys`); retired SEKs stay openable by version. SIK rotation is deferred. `key_audit` is store-only in v1.
  - **`key_share`** (metadata escrow) payload adds `target_sign_pub`: the target device's VD1 Ed25519 public key, which is also its snapshot signing key (`device.sign_pub`).
  - **`rewrap {repo_id, epoch}`**: rotate to `epoch` when the repo is at `epoch-1`; report done when already at or past it. Revoked devices are excluded using the device's own view of revoked devices.
  - **Issue classes.** Desktop-local reasons `efs_needs_logon` and `profile_needs_logon` are reported as `access_denied` and `other` with the reason in `detail`.
  - **Issue routes.** `GET /backup/runs/{id}/issues`, `POST …/issues/skip` and `POST …/retry-skipped` accept sessions and device signatures (`user` auth). A device may act only on its own device's runs; others get `404`.
- **Rev 1.1c (2026-10-18, INT), Phase 2 restore.** Additive only.
  - **Feature flag `restore.v1`.** Advertised when the restore routes below and `/restore/*` are implemented.
  - **Plan.**
    - **Fields:** `POST /restore/plan` takes `selection` paths as snapshot token paths (for example `{DOCUMENTS}/Reports`). An empty selection means the whole snapshot.
    - **Who may plan:** the snapshot must belong to the caller, and the target device to the same user (else `404`).
    - **Target profile:** it must be `authorised` or `active_default` on the target device (`403 profile_not_authorised`, `423 profile_blacklisted`). With none given, the snapshot's own profile is used.
    - **Catalog:** the plan is computed from the encrypted catalog, so it works under both escrow modes. Before ingestion is done the reply is `409 catalog_pending`.
    - **Mappings:** one per token used by the selection, with `auto:true` and an empty `to`. The device resolves them for the target profile. `mapping.overrides[{from,to}]` map a token path prefix to an absolute target folder.
    - **Conflicts:** the server never computes them (`summary.conflicts` is 0). The device decides per file and reports items.
  - **Execute.** `POST /restore/execute {plan_token}` creates the restore (`awaiting_approval`) and an approval of kind `restore` decided by the owner. On approval, the restore becomes `queued` and the server queues a `restore` device task (payload `{restore_id}`). On reject or expiry it becomes `cancelled`.
  - **Device routes (dev).** The device must be the restore's target device; anyone else gets `404`.
    - `GET /device/restores/{id}` → `DeviceRestore {restore, repo_id, repo_uuid, snapshot_oid, selection, mappings, overrides, target_profile_sid, conflict_policy, cancel_requested}`.
    - `POST /device/restores/{id}/progress` with `RestoreProgress {status, files_total, files_done, bytes_total, bytes_done, error?, items[≤1000]}` → `{status, cancel_requested}`.
      - `status` is `running` or a terminal status: `completed`, `partial`, `failed` or `cancelled`.
      - A report on a terminal restore is `409 conflict`.
      - Items are appended. Item `status` is `pending`, `done`, `skipped`, `failed` or `blocked` (system-file protection, §10.3).
    - The device acknowledges the `restore` task as soon as it starts. Progress then goes only through these routes, because a restore can outlive the task lease.
  - **Cancel.** `POST /restore/{id}/cancel` (sess):
    - `awaiting_approval` or `queued` → `cancelled` (the pending approval and task are cancelled);
    - `running` → `cancel_requested:true`, which the device sees on its next progress reply;
    - a terminal restore → `409 conflict`.
  - **Restore fields.** `Restore` adds `conflict_policy`, `cancel_requested`, `error` and `finished_at`. Item `size` is optional.
  - **Snapshot content and download (full escrow).**
    - `GET /snapshots/{id}/content?path=` streams one file. It supports Range, sends `Content-Disposition: attachment` and `X-Content-Type-Options: nosniff`, and verifies the keyed whole-file hash.
    - `POST /snapshots/{id}/download {paths[1..100]}` (folders are expanded) → `202 {operation_id}`, operation kind `snapshot_download`. Its result is `SnapshotDownloadResult {download_url, expires_at, files, bytes}`.
    - `download_url` is `/api/v1/downloads/{token}`: unauthenticated, single-use and valid for 1 h. It serves the zip.
    - **Limits:** at most 100,000 files and 50 GiB per zip.
    - **Escrow:** both routes need full escrow (`409 escrow_unavailable`). Every server unwrap is audited with purpose `download`.


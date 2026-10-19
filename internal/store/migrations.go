package store

// migrations are applied in order inside a transaction each. Never edit a
// released migration; append a new one instead.
var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE storage_locations (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL UNIQUE,
	path          TEXT NOT NULL UNIQUE,
	reserve_bytes INTEGER NOT NULL DEFAULT 0,
	enabled       INTEGER NOT NULL DEFAULT 1,
	is_default    INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);

CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	email         TEXT UNIQUE COLLATE NOCASE,
	display_name  TEXT NOT NULL DEFAULT '',
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
	disabled      INTEGER NOT NULL DEFAULT 0,
	quota_bytes   INTEGER NOT NULL DEFAULT 0,
	used_bytes    INTEGER NOT NULL DEFAULT 0,
	location_id   INTEGER REFERENCES storage_locations(id),
	root_id       INTEGER,
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL,
	last_login_at INTEGER
);

CREATE TABLE sessions (
	id           TEXT PRIMARY KEY,
	user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf         TEXT NOT NULL,
	ip           TEXT NOT NULL DEFAULT '',
	user_agent   TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE nodes (
	id                 INTEGER PRIMARY KEY,
	user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	parent_id          INTEGER REFERENCES nodes(id),
	name               TEXT NOT NULL,
	is_dir             INTEGER NOT NULL,
	size               INTEGER NOT NULL DEFAULT 0,
	sha256             TEXT,
	mime               TEXT NOT NULL DEFAULT '',
	created_at         INTEGER NOT NULL,
	modified_at        INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	deleted_at         INTEGER,
	trash_root         INTEGER NOT NULL DEFAULT 0,
	original_parent_id INTEGER,
	origin_device_id   INTEGER
);
CREATE UNIQUE INDEX nodes_unique_name ON nodes(user_id, parent_id, name COLLATE NOCASE) WHERE deleted_at IS NULL;
CREATE INDEX nodes_parent ON nodes(parent_id);
CREATE INDEX nodes_user_sha ON nodes(user_id, sha256);
CREATE INDEX nodes_trash ON nodes(user_id, deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX nodes_user_name ON nodes(user_id, name COLLATE NOCASE);

CREATE TABLE node_tags (
	node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	tag     TEXT NOT NULL COLLATE NOCASE,
	PRIMARY KEY (node_id, tag)
);
CREATE INDEX node_tags_tag ON node_tags(tag);

CREATE TABLE blobs (
	user_id     INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	size        INTEGER NOT NULL,
	location_id INTEGER NOT NULL REFERENCES storage_locations(id),
	refcount    INTEGER NOT NULL,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (user_id, sha256)
);

CREATE TABLE devices (
	id               INTEGER PRIMARY KEY,
	user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name             TEXT NOT NULL,
	os               TEXT NOT NULL DEFAULT '',
	client_version   TEXT NOT NULL DEFAULT '',
	identifier       TEXT NOT NULL,
	public_key       TEXT NOT NULL,
	key_fingerprint  TEXT NOT NULL,
	status           TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','revoked','expired')),
	user_code        TEXT NOT NULL,
	poll_token_hash  TEXT,
	request_ip       TEXT NOT NULL DEFAULT '',
	request_location TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	expires_at       INTEGER NOT NULL,
	decided_at       INTEGER,
	decided_by       INTEGER,
	last_seen_at     INTEGER,
	last_ip          TEXT NOT NULL DEFAULT '',
	sync_state       TEXT NOT NULL DEFAULT '{}',
	sync_state_at    INTEGER
);
CREATE INDEX devices_user ON devices(user_id, status);

CREATE TABLE uploads (
	id             TEXT PRIMARY KEY,
	user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id      INTEGER,
	transfer_id    INTEGER,
	path           TEXT NOT NULL,
	size           INTEGER NOT NULL,
	sha256         TEXT NOT NULL DEFAULT '',
	chunk_size     INTEGER NOT NULL,
	chunks_total   INTEGER NOT NULL,
	received       BLOB NOT NULL,
	bytes_received INTEGER NOT NULL DEFAULT 0,
	location_id    INTEGER NOT NULL,
	modified_at    INTEGER NOT NULL,
	tags           TEXT NOT NULL DEFAULT '[]',
	on_conflict    TEXT NOT NULL DEFAULT 'replace',
	status         TEXT NOT NULL DEFAULT 'open',
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	expires_at     INTEGER NOT NULL
);
CREATE INDEX uploads_user ON uploads(user_id, status);

CREATE TABLE transfers (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id   INTEGER,
	upload_id   TEXT,
	path        TEXT NOT NULL,
	size        INTEGER NOT NULL DEFAULT 0,
	bytes_done  INTEGER NOT NULL DEFAULT 0,
	status      TEXT NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT 'web',
	started_at  INTEGER NOT NULL,
	finished_at INTEGER
);
CREATE INDEX transfers_user ON transfers(user_id, started_at);
CREATE INDEX transfers_status ON transfers(status);

CREATE TABLE automations (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	enabled     INTEGER NOT NULL DEFAULT 0,
	priority    INTEGER NOT NULL DEFAULT 0,
	is_template INTEGER NOT NULL DEFAULT 0,
	valid       INTEGER NOT NULL DEFAULT 0,
	definition  TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);
CREATE INDEX automations_user ON automations(user_id);

CREATE TABLE activity (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER,
	actor_id   INTEGER,
	device_id  INTEGER,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	details    TEXT NOT NULL DEFAULT '',
	ip         TEXT NOT NULL DEFAULT '',
	audit      INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX activity_user ON activity(user_id, id);
CREATE INDEX activity_audit ON activity(audit, id);

CREATE TABLE notifications (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title      TEXT NOT NULL,
	message    TEXT NOT NULL DEFAULT '',
	level      TEXT NOT NULL DEFAULT 'info',
	source     TEXT NOT NULL DEFAULT '',
	read_at    INTEGER,
	created_at INTEGER NOT NULL
);
CREATE INDEX notifications_user ON notifications(user_id, id);
`,

	// 2: secrets, destinations, approvals, operations, plans, device delete
	// permission, superuser flag (api-v1 §1, §3, §4, §8, §11B)
	`
ALTER TABLE devices ADD COLUMN can_delete INTEGER NOT NULL DEFAULT 1;

ALTER TABLE users ADD COLUMN is_superuser INTEGER NOT NULL DEFAULT 0;
UPDATE users SET is_superuser = 1
 WHERE id = (SELECT id FROM users WHERE role = 'admin' ORDER BY created_at, id LIMIT 1);
-- The first administrator ever created (fresh install) becomes the superuser.
CREATE TRIGGER users_first_superuser AFTER INSERT ON users
WHEN NEW.role = 'admin' AND NOT EXISTS (SELECT 1 FROM users WHERE is_superuser = 1)
BEGIN
	UPDATE users SET is_superuser = 1 WHERE id = NEW.id;
END;

-- Envelope-encrypted secrets (destination credentials, AI keys). The KEK
-- lives outside the database; key_id names the KEK version used.
CREATE TABLE secrets (
	id            INTEGER PRIMARY KEY,
	owner_user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
	kind          TEXT NOT NULL,
	key_id        TEXT NOT NULL,
	nonce         BLOB NOT NULL,
	ciphertext    BLOB NOT NULL,
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL,
	rotated_at    INTEGER
);
CREATE INDEX secrets_owner ON secrets(owner_user_id);

CREATE TABLE destinations (
	id              INTEGER PRIMARY KEY,
	scope           TEXT NOT NULL CHECK (scope IN ('user','global')),
	user_id         INTEGER REFERENCES users(id) ON DELETE CASCADE,
	name            TEXT NOT NULL,
	kind            TEXT NOT NULL CHECK (kind IN ('server','local','s3')),
	config          TEXT NOT NULL DEFAULT '{}',
	secret_id       INTEGER REFERENCES secrets(id) ON DELETE SET NULL,
	enabled         INTEGER NOT NULL DEFAULT 1,
	health          TEXT NOT NULL DEFAULT 'unknown' CHECK (health IN ('ok','degraded','unreachable','unknown')),
	last_checked_at INTEGER,
	capabilities    TEXT NOT NULL DEFAULT '{}',
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL,
	CHECK ((scope = 'global' AND user_id IS NULL) OR (scope = 'user' AND user_id IS NOT NULL))
);
CREATE UNIQUE INDEX destinations_name ON destinations(COALESCE(user_id, 0), name COLLATE NOCASE);
CREATE INDEX destinations_user ON destinations(user_id);

CREATE TABLE approvals (
	id                     INTEGER PRIMARY KEY,
	user_id                INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind                   TEXT NOT NULL,
	risk                   TEXT NOT NULL DEFAULT 'medium' CHECK (risk IN ('low','medium','high')),
	status                 TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','cancelled','executed','failed')),
	title                  TEXT NOT NULL DEFAULT '',
	summary                TEXT NOT NULL DEFAULT '',
	payload                TEXT NOT NULL DEFAULT '{}',
	target_type            TEXT NOT NULL DEFAULT '',
	target_id              INTEGER,
	source                 TEXT NOT NULL DEFAULT 'user',
	requested_by_user_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	requested_by_device_id INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	operation_id           INTEGER,
	decided_by             INTEGER REFERENCES users(id) ON DELETE SET NULL,
	decided_at             INTEGER,
	decision_note          TEXT NOT NULL DEFAULT '',
	created_at             INTEGER NOT NULL,
	updated_at             INTEGER NOT NULL,
	expires_at             INTEGER
);
CREATE INDEX approvals_user ON approvals(user_id, status, id);
CREATE INDEX approvals_status ON approvals(status, expires_at);

CREATE TABLE operations (
	id             INTEGER PRIMARY KEY,
	user_id        INTEGER REFERENCES users(id) ON DELETE CASCADE,
	device_id      INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	kind           TEXT NOT NULL,
	state          TEXT NOT NULL DEFAULT 'queued',
	progress_done  INTEGER NOT NULL DEFAULT 0,
	progress_total INTEGER NOT NULL DEFAULT 0,
	progress_bytes INTEGER NOT NULL DEFAULT 0,
	params         TEXT NOT NULL DEFAULT '{}',
	result         TEXT NOT NULL DEFAULT '{}',
	error          TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	finished_at    INTEGER
);
CREATE INDEX operations_user ON operations(user_id, id);
CREATE INDEX operations_state ON operations(state, kind);

-- Plan/execute tokens (api-v1 §1). Only a hash of the token is stored.
CREATE TABLE plans (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	target_type TEXT NOT NULL DEFAULT '',
	target_id   INTEGER,
	plan        TEXT NOT NULL,
	plan_digest TEXT NOT NULL,
	token_hash  TEXT NOT NULL UNIQUE,
	status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','executed','stale','expired')),
	created_at  INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	executed_at INTEGER
);
CREATE INDEX plans_user ON plans(user_id, kind);
CREATE INDEX plans_expiry ON plans(expires_at);
`,

	// 3: repositories, objects, snapshots and the encrypted catalog
	// (api-v1 §5, §5.4)
	`
CREATE TABLE repositories (
	id                   INTEGER PRIMARY KEY,
	user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	destination_id       INTEGER NOT NULL REFERENCES destinations(id),
	repo_uuid            TEXT NOT NULL UNIQUE,
	name                 TEXT NOT NULL,
	format_version       INTEGER NOT NULL DEFAULT 1,
	escrow_mode          TEXT NOT NULL DEFAULT 'full' CHECK (escrow_mode IN ('full','metadata')),
	current_epoch        INTEGER NOT NULL DEFAULT 1,
	created_by_device_id INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	last_catalog_sync_at INTEGER,
	created_at           INTEGER NOT NULL,
	updated_at           INTEGER NOT NULL,
	UNIQUE (user_id, destination_id)
);
CREATE INDEX repositories_destination ON repositories(destination_id);

-- delete_pending_at marks objects queued for GC; they count as missing.
CREATE TABLE repo_objects (
	id                INTEGER PRIMARY KEY,
	repo_id           INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	kind              TEXT NOT NULL CHECK (kind IN ('config','keys','chain','index','snapshots','data','locks')),
	oid               TEXT NOT NULL,
	size              INTEGER NOT NULL,
	created_at        INTEGER NOT NULL,
	last_verified_at  INTEGER,
	verify_status     TEXT NOT NULL DEFAULT '',
	delete_pending_at INTEGER,
	UNIQUE (repo_id, kind, oid)
);
CREATE INDEX repo_objects_pending ON repo_objects(repo_id, delete_pending_at) WHERE delete_pending_at IS NOT NULL;

CREATE TABLE snapshots (
	id                 INTEGER PRIMARY KEY,
	repo_id            INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id          INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	snapshot_oid       TEXT NOT NULL,
	job_id             INTEGER,
	run_id             INTEGER,
	parent_snapshot_id INTEGER REFERENCES snapshots(id) ON DELETE SET NULL,
	kind               TEXT NOT NULL DEFAULT '',
	mode               TEXT NOT NULL DEFAULT '',
	hostname           TEXT NOT NULL DEFAULT '',
	os_user            TEXT NOT NULL DEFAULT '',
	profile_sid        TEXT NOT NULL DEFAULT '',
	profile_username   TEXT NOT NULL DEFAULT '',
	profile_path       TEXT NOT NULL DEFAULT '',
	profile_consent_id TEXT NOT NULL DEFAULT '',
	epoch              INTEGER NOT NULL DEFAULT 1,
	client_time        INTEGER NOT NULL DEFAULT 0,
	committed_at       INTEGER NOT NULL,
	tags               TEXT NOT NULL DEFAULT '[]',
	stats              TEXT NOT NULL DEFAULT '{}',
	index_oids         TEXT NOT NULL DEFAULT '[]',
	file_count         INTEGER NOT NULL DEFAULT 0,
	logical_bytes      INTEGER NOT NULL DEFAULT 0,
	new_bytes          INTEGER NOT NULL DEFAULT 0,
	protected          INTEGER NOT NULL DEFAULT 0,
	protected_until    INTEGER,
	status             TEXT NOT NULL DEFAULT 'committed' CHECK (status IN ('committed','forgotten','missing')),
	ingest_state       TEXT NOT NULL DEFAULT 'pending' CHECK (ingest_state IN ('pending','done','failed')),
	ingest_error       TEXT NOT NULL DEFAULT '',
	UNIQUE (repo_id, snapshot_oid)
);
CREATE INDEX snapshots_user ON snapshots(user_id, committed_at);
CREATE INDEX snapshots_job ON snapshots(job_id, committed_at);
CREATE INDEX snapshots_device ON snapshots(device_id, committed_at);
CREATE INDEX snapshots_ingest ON snapshots(ingest_state) WHERE ingest_state = 'pending';

CREATE TABLE snapshot_packs (
	snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
	object_id   INTEGER NOT NULL REFERENCES repo_objects(id),
	PRIMARY KEY (snapshot_id, object_id)
) WITHOUT ROWID;
CREATE INDEX snapshot_packs_object ON snapshot_packs(object_id);

-- Catalog rows: paths and names are encrypted with the per-user catalog DEK.
-- path_bi/parent_bi are HMAC blind indexes for tree browsing; blind_tokens
-- holds the name tokens, mirrored into snapshot_file_tokens for search.
CREATE TABLE snapshot_files (
	id           INTEGER PRIMARY KEY,
	snapshot_id  INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
	path_bi      BLOB NOT NULL,
	parent_bi    BLOB,
	path_ct      BLOB NOT NULL,
	name_ct      BLOB NOT NULL,
	blind_tokens BLOB NOT NULL DEFAULT x'',
	type         TEXT NOT NULL DEFAULT 'file',
	size         INTEGER NOT NULL DEFAULT 0,
	mtime        INTEGER NOT NULL DEFAULT 0,
	mode         INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX snapshot_files_path ON snapshot_files(snapshot_id, path_bi);
CREATE INDEX snapshot_files_parent ON snapshot_files(snapshot_id, parent_bi);

CREATE TABLE snapshot_file_tokens (
	snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
	token       BLOB NOT NULL,
	file_id     INTEGER NOT NULL REFERENCES snapshot_files(id) ON DELETE CASCADE,
	PRIMARY KEY (snapshot_id, token, file_id)
) WITHOUT ROWID;
CREATE INDEX snapshot_file_tokens_file ON snapshot_file_tokens(file_id);

CREATE TABLE catalog_keys (
	user_id            INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	kek_id             TEXT NOT NULL DEFAULT '',
	dek_wrapped_by_kek BLOB NOT NULL,
	created_at         INTEGER NOT NULL,
	rotated_at         INTEGER
);
`,

	// 4: jobs, runs, issues, device tasks, inventory, restores, backup state
	// (api-v1 §6, §7, §11)
	`
CREATE TABLE backup_jobs (
	id                  INTEGER PRIMARY KEY,
	user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id           INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	repo_id             INTEGER REFERENCES repositories(id) ON DELETE SET NULL,
	name                TEXT NOT NULL,
	kind                TEXT NOT NULL,
	mode                TEXT NOT NULL DEFAULT '',
	spec                TEXT NOT NULL DEFAULT '{}',
	schedule            TEXT NOT NULL DEFAULT '{}',
	retention_policy_id INTEGER,
	priority            INTEGER NOT NULL DEFAULT 0,
	enabled             INTEGER NOT NULL DEFAULT 1,
	profile_sid         TEXT NOT NULL DEFAULT '',
	required_modules    TEXT NOT NULL DEFAULT '[]',
	state               TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','suspended_by_mode','suspended_by_profile')),
	revision            INTEGER NOT NULL DEFAULT 1,
	created_at          INTEGER NOT NULL,
	updated_at          INTEGER NOT NULL
);
CREATE INDEX backup_jobs_user ON backup_jobs(user_id);
CREATE INDEX backup_jobs_device ON backup_jobs(device_id, state);

CREATE TABLE job_runs (
	id              INTEGER PRIMARY KEY,
	job_id          INTEGER NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
	user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id       INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	client_run_uuid TEXT NOT NULL,
	snapshot_id     INTEGER REFERENCES snapshots(id) ON DELETE SET NULL,
	source          TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL DEFAULT 'queued',
	started_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL,
	finished_at     INTEGER,
	files_total     INTEGER NOT NULL DEFAULT 0,
	files_done      INTEGER NOT NULL DEFAULT 0,
	bytes_total     INTEGER NOT NULL DEFAULT 0,
	bytes_done      INTEGER NOT NULL DEFAULT 0,
	bytes_new       INTEGER NOT NULL DEFAULT 0,
	bytes_uploaded  INTEGER NOT NULL DEFAULT 0,
	warnings        INTEGER NOT NULL DEFAULT 0,
	errors          INTEGER NOT NULL DEFAULT 0,
	skipped         INTEGER NOT NULL DEFAULT 0,
	summary         TEXT NOT NULL DEFAULT '{}',
	error           TEXT NOT NULL DEFAULT '',
	UNIQUE (job_id, client_run_uuid)
);
CREATE INDEX job_runs_job ON job_runs(job_id, started_at);
CREATE INDEX job_runs_user ON job_runs(user_id, started_at);
CREATE INDEX job_runs_status ON job_runs(status, finished_at);

CREATE TABLE run_issues (
	id           INTEGER PRIMARY KEY,
	run_id       INTEGER NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
	path         TEXT NOT NULL DEFAULT '',
	severity     TEXT NOT NULL CHECK (severity IN ('warning','skipped','error')),
	class        TEXT NOT NULL DEFAULT '',
	message      TEXT NOT NULL DEFAULT '',
	retryable    INTEGER NOT NULL DEFAULT 0,
	skip         INTEGER NOT NULL DEFAULT 0,
	resolved_at  INTEGER,
	retry_run_id INTEGER REFERENCES job_runs(id) ON DELETE SET NULL,
	created_at   INTEGER NOT NULL
);
CREATE INDEX run_issues_run ON run_issues(run_id, severity);

CREATE TABLE device_tasks (
	id               INTEGER PRIMARY KEY,
	user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id        INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	kind             TEXT NOT NULL,
	payload          TEXT NOT NULL DEFAULT '{}',
	required_modules TEXT NOT NULL DEFAULT '[]',
	status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','awaiting_approval','claimed','done','failed','cancelled','expired')),
	approval_id      INTEGER REFERENCES approvals(id) ON DELETE SET NULL,
	created_by       INTEGER REFERENCES users(id) ON DELETE SET NULL,
	attempts         INTEGER NOT NULL DEFAULT 0,
	result           TEXT NOT NULL DEFAULT '{}',
	error_code       TEXT NOT NULL DEFAULT '',
	error            TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	claimed_at       INTEGER,
	finished_at      INTEGER,
	expires_at       INTEGER
);
CREATE INDEX device_tasks_device ON device_tasks(device_id, status, id);
CREATE INDEX device_tasks_user ON device_tasks(user_id, id);

-- Searchable projection of the latest device inventory (discovery module).
CREATE TABLE app_inventory (
	id            INTEGER PRIMARY KEY,
	device_id     INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	snapshot_id   INTEGER REFERENCES snapshots(id) ON DELETE SET NULL,
	name          TEXT NOT NULL,
	version       TEXT NOT NULL DEFAULT '',
	publisher     TEXT NOT NULL DEFAULT '',
	source        TEXT NOT NULL DEFAULT '',
	package_id    TEXT NOT NULL DEFAULT '',
	install_scope TEXT NOT NULL DEFAULT '',
	mode          TEXT NOT NULL DEFAULT '',
	details       TEXT NOT NULL DEFAULT '{}',
	updated_at    INTEGER NOT NULL
);
CREATE INDEX app_inventory_device ON app_inventory(device_id, name COLLATE NOCASE);

CREATE TABLE restores (
	id                 INTEGER PRIMARY KEY,
	user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id          INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	snapshot_id        INTEGER REFERENCES snapshots(id) ON DELETE SET NULL,
	target_profile_sid TEXT NOT NULL DEFAULT '',
	target_path        TEXT NOT NULL DEFAULT '',
	mode               TEXT NOT NULL DEFAULT '',
	reinstall_apps     INTEGER NOT NULL DEFAULT 0,
	options            TEXT NOT NULL DEFAULT '{}',
	plan               TEXT NOT NULL DEFAULT '{}',
	guard              TEXT NOT NULL DEFAULT '[]',
	status             TEXT NOT NULL DEFAULT 'planned',
	task_id            INTEGER REFERENCES device_tasks(id) ON DELETE SET NULL,
	approval_id        INTEGER REFERENCES approvals(id) ON DELETE SET NULL,
	operation_id       INTEGER REFERENCES operations(id) ON DELETE SET NULL,
	summary            TEXT NOT NULL DEFAULT '{}',
	error              TEXT NOT NULL DEFAULT '',
	created_by         INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	finished_at        INTEGER
);
CREATE INDEX restores_user ON restores(user_id, id);
CREATE INDEX restores_device ON restores(device_id, status);

CREATE TABLE restore_items (
	id         INTEGER PRIMARY KEY,
	restore_id INTEGER NOT NULL REFERENCES restores(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	kind       TEXT NOT NULL DEFAULT 'file',
	action     TEXT NOT NULL DEFAULT 'restore',
	status     TEXT NOT NULL DEFAULT 'pending',
	warning    TEXT NOT NULL DEFAULT '',
	error      TEXT NOT NULL DEFAULT '',
	bytes      INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);
CREATE INDEX restore_items_restore ON restore_items(restore_id, status);

CREATE TABLE device_backup_state (
	device_id  INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
	state      TEXT NOT NULL DEFAULT '{}',
	updated_at INTEGER NOT NULL
);
`,

	// 5: retention, prune plans, repository locks, server schedules (api-v1 §9)
	`
CREATE TABLE retention_policies (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	rules      TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (user_id, name)
);

CREATE TABLE prune_plans (
	id           INTEGER PRIMARY KEY,
	repo_id      INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	policy_id    INTEGER REFERENCES retention_policies(id) ON DELETE SET NULL,
	created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	source       TEXT NOT NULL DEFAULT 'user',
	status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','approved','executing','done','failed','expired')),
	plan         TEXT NOT NULL DEFAULT '{}',
	approval_id  INTEGER REFERENCES approvals(id) ON DELETE SET NULL,
	approved_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
	approved_at  INTEGER,
	operation_id INTEGER REFERENCES operations(id) ON DELETE SET NULL,
	executed_at  INTEGER,
	error        TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	expires_at   INTEGER
);
CREATE INDEX prune_plans_repo ON prune_plans(repo_id, status);

-- Server API locks are authoritative (backup-format §10).
CREATE TABLE repo_locks (
	id               TEXT PRIMARY KEY,
	repo_id          INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	kind             TEXT NOT NULL CHECK (kind IN ('shared','exclusive')),
	holder_kind      TEXT NOT NULL CHECK (holder_kind IN ('device','server')),
	holder_device_id INTEGER REFERENCES devices(id) ON DELETE CASCADE,
	holder           TEXT NOT NULL DEFAULT '{}',
	acquired_at      INTEGER NOT NULL,
	refreshed_at     INTEGER NOT NULL,
	expires_at       INTEGER NOT NULL
);
CREATE INDEX repo_locks_repo ON repo_locks(repo_id, expires_at);

-- Server-side recurring work (prune, verify, replication, DB backup).
CREATE TABLE schedules (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER REFERENCES users(id) ON DELETE CASCADE,
	target_kind TEXT NOT NULL,
	target_id   INTEGER,
	spec        TEXT NOT NULL DEFAULT '{}',
	enabled     INTEGER NOT NULL DEFAULT 1,
	next_run_at INTEGER,
	last_run_at INTEGER,
	last_status TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);
CREATE INDEX schedules_due ON schedules(enabled, next_run_at);
CREATE INDEX schedules_target ON schedules(target_kind, target_id);
`,

	// 6: file versions, sync folders, conflicts, change feed (api-v1 §10)
	`
ALTER TABLE nodes ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;

-- Each version holds a blob reference like a node does (blobs.refcount).
CREATE TABLE file_versions (
	id               INTEGER PRIMARY KEY,
	user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	node_id          INTEGER NOT NULL REFERENCES nodes(id),
	revision         INTEGER NOT NULL DEFAULT 0,
	sha256           TEXT NOT NULL,
	size             INTEGER NOT NULL,
	modified_at      INTEGER NOT NULL,
	origin_device_id INTEGER,
	pinned           INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL
);
CREATE INDEX file_versions_node ON file_versions(node_id, created_at);
CREATE INDEX file_versions_user_sha ON file_versions(user_id, sha256);

CREATE TABLE sync_folders (
	id                    INTEGER PRIMARY KEY,
	user_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	device_id             INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	profile_sid           TEXT NOT NULL DEFAULT '',
	local_path            TEXT NOT NULL,
	remote_path           TEXT NOT NULL,
	direction             TEXT NOT NULL DEFAULT 'upload' CHECK (direction IN ('upload','two_way')),
	conflict_policy       TEXT NOT NULL DEFAULT 'keep_both' CHECK (conflict_policy IN ('keep_both','newer_wins','local_wins','remote_wins','ask')),
	delete_policy         TEXT NOT NULL DEFAULT 'propagate_to_trash' CHECK (delete_policy IN ('propagate_to_trash','ignore')),
	mass_delete_files     INTEGER NOT NULL DEFAULT 50,
	mass_delete_percent   INTEGER NOT NULL DEFAULT 10,
	ruleset               TEXT NOT NULL DEFAULT '{}',
	schedule              TEXT NOT NULL DEFAULT '{}',
	enabled               INTEGER NOT NULL DEFAULT 1,
	revision              INTEGER NOT NULL DEFAULT 1,
	acked_revision        INTEGER NOT NULL DEFAULT 0,
	state                 TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','paused','suspended_by_mode','suspended_by_profile')),
	created_at            INTEGER NOT NULL,
	updated_at            INTEGER NOT NULL
);
CREATE UNIQUE INDEX sync_folders_local ON sync_folders(device_id, local_path COLLATE NOCASE);
CREATE INDEX sync_folders_user ON sync_folders(user_id);

ALTER TABLE uploads ADD COLUMN folder_id INTEGER REFERENCES sync_folders(id) ON DELETE SET NULL;
ALTER TABLE uploads ADD COLUMN base_revision INTEGER;
ALTER TABLE uploads ADD COLUMN base_sha256 TEXT NOT NULL DEFAULT '';

CREATE TABLE sync_conflicts (
	id                 INTEGER PRIMARY KEY,
	user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	folder_id          INTEGER REFERENCES sync_folders(id) ON DELETE CASCADE,
	device_id          INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	node_id            INTEGER,
	conflict_node_id   INTEGER,
	path               TEXT NOT NULL,
	policy             TEXT NOT NULL DEFAULT '',
	local_sha256       TEXT NOT NULL DEFAULT '',
	local_size         INTEGER NOT NULL DEFAULT 0,
	local_modified_at  INTEGER,
	remote_revision    INTEGER,
	remote_sha256      TEXT NOT NULL DEFAULT '',
	remote_size        INTEGER NOT NULL DEFAULT 0,
	remote_modified_at INTEGER,
	state              TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','resolved')),
	resolution         TEXT NOT NULL DEFAULT '' CHECK (resolution IN ('','keep_local','keep_remote','keep_both')),
	resolved_by        INTEGER REFERENCES users(id) ON DELETE SET NULL,
	resolved_at        INTEGER,
	created_at         INTEGER NOT NULL
);
CREATE INDEX sync_conflicts_user ON sync_conflicts(user_id, state, id);
CREATE INDEX sync_conflicts_folder ON sync_conflicts(folder_id, state);

-- Per-user change feed; seq is monotonic per user. Tombstones are pruned by
-- created_at after the retention window.
CREATE TABLE sync_changes (
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	seq         INTEGER NOT NULL,
	node_id     INTEGER NOT NULL,
	op          TEXT NOT NULL CHECK (op IN ('create','update','move','delete')),
	path        TEXT NOT NULL,
	old_path    TEXT NOT NULL DEFAULT '',
	revision    INTEGER NOT NULL,
	sha256      TEXT NOT NULL DEFAULT '',
	size        INTEGER NOT NULL DEFAULT 0,
	modified_at INTEGER NOT NULL,
	device_id   INTEGER,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (user_id, seq)
) WITHOUT ROWID;
CREATE INDEX sync_changes_age ON sync_changes(created_at);
`,

	// 7: replication, notification preferences, AI, quotas, automation
	// runtime (api-v1 §9, §11)
	`
CREATE TABLE replication_policies (
	id                 INTEGER PRIMARY KEY,
	user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	src_repo_id        INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	dst_destination_id INTEGER NOT NULL REFERENCES destinations(id),
	mode               TEXT NOT NULL DEFAULT 'copy-new' CHECK (mode IN ('mirror','copy-new')),
	schedule           TEXT NOT NULL DEFAULT '{}',
	enabled            INTEGER NOT NULL DEFAULT 1,
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	UNIQUE (src_repo_id, dst_destination_id)
);
CREATE INDEX replication_policies_dest ON replication_policies(dst_destination_id);

CREATE TABLE replication_state (
	policy_id       INTEGER PRIMARY KEY REFERENCES replication_policies(id) ON DELETE CASCADE,
	status          TEXT NOT NULL DEFAULT 'idle',
	last_cursor     TEXT NOT NULL DEFAULT '',
	last_run_at     INTEGER,
	last_success_at INTEGER,
	objects_copied  INTEGER NOT NULL DEFAULT 0,
	bytes_copied    INTEGER NOT NULL DEFAULT 0,
	lag_objects     INTEGER NOT NULL DEFAULT 0,
	error           TEXT NOT NULL DEFAULT '',
	updated_at      INTEGER NOT NULL
);

CREATE TABLE notification_prefs (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	event   TEXT NOT NULL,
	channel TEXT NOT NULL CHECK (channel IN ('in_app','email','desktop')),
	enabled INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (user_id, event, channel)
) WITHOUT ROWID;

CREATE TABLE ai_settings (
	user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	enabled    INTEGER NOT NULL DEFAULT 0,
	base_url   TEXT NOT NULL DEFAULT '',
	model      TEXT NOT NULL DEFAULT '',
	secret_id  INTEGER REFERENCES secrets(id) ON DELETE SET NULL,
	scopes     TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

-- One row per outbound AI call; never stores prompts or the API key.
CREATE TABLE ai_audit (
	id             INTEGER PRIMARY KEY,
	user_id        INTEGER REFERENCES users(id) ON DELETE CASCADE,
	purpose        TEXT NOT NULL,
	endpoint_host  TEXT NOT NULL DEFAULT '',
	model          TEXT NOT NULL DEFAULT '',
	request_digest TEXT NOT NULL DEFAULT '',
	redactions     INTEGER NOT NULL DEFAULT 0,
	status         TEXT NOT NULL,
	error          TEXT NOT NULL DEFAULT '',
	proposals      INTEGER NOT NULL DEFAULT 0,
	tokens_in      INTEGER NOT NULL DEFAULT 0,
	tokens_out     INTEGER NOT NULL DEFAULT 0,
	duration_ms    INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL
);
CREATE INDEX ai_audit_user ON ai_audit(user_id, id);

ALTER TABLE users ADD COLUMN backup_quota_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE automations ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE automations ADD COLUMN runtime TEXT NOT NULL DEFAULT 'device';
`,

	// 8: server keys and key audit, device keys, modules, Windows profiles
	// (api-v1 §5A, §6A, §6B; backup-format §4.6)
	`
-- Private keys are stored only sealed by the server KEK.
CREATE TABLE server_keys (
	id                    INTEGER PRIMARY KEY,
	kind                  TEXT NOT NULL CHECK (kind IN ('sik','sek')),
	version               INTEGER NOT NULL,
	public                BLOB NOT NULL,
	private_sealed_by_kek BLOB NOT NULL,
	fingerprint           TEXT NOT NULL DEFAULT '',
	signature             BLOB,
	created_at            INTEGER NOT NULL,
	retired_at            INTEGER,
	UNIQUE (kind, version)
);
CREATE UNIQUE INDEX server_keys_current ON server_keys(kind) WHERE retired_at IS NULL;

CREATE TABLE key_audit (
	id              INTEGER PRIMARY KEY,
	action          TEXT NOT NULL,
	purpose         TEXT NOT NULL DEFAULT '',
	actor_user_id   INTEGER,
	actor_device_id INTEGER,
	user_id         INTEGER,
	repo_id         INTEGER,
	epoch           INTEGER,
	key_kind        TEXT NOT NULL DEFAULT '',
	key_version     INTEGER,
	operation_id    INTEGER,
	details         TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL
);
CREATE INDEX key_audit_repo ON key_audit(repo_id, id);
CREATE INDEX key_audit_user ON key_audit(user_id, id);

-- Server-side projection of the plaintext key files (keys/<oid>) for
-- /repos/{id}/keys/status.
CREATE TABLE repo_keys (
	repo_id       INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
	key_file_oid  TEXT NOT NULL,
	key_id        TEXT NOT NULL,
	kind          TEXT NOT NULL CHECK (kind IN ('device','escrow','recovery','passphrase')),
	epoch         INTEGER NOT NULL,
	label         TEXT NOT NULL DEFAULT '',
	device_id     INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	recipient_pub TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	PRIMARY KEY (repo_id, key_file_oid)
);
CREATE INDEX repo_keys_epoch ON repo_keys(repo_id, epoch, kind);

CREATE TABLE device_keys (
	device_id              INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
	box_pub                TEXT NOT NULL,
	sik_pinned_fingerprint TEXT NOT NULL DEFAULT '',
	created_at             INTEGER NOT NULL,
	updated_at             INTEGER NOT NULL
);

CREATE TABLE device_modules (
	device_id    INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
	install_mode TEXT NOT NULL DEFAULT 'simple' CHECK (install_mode IN ('simple','advanced')),
	core_version TEXT NOT NULL DEFAULT '',
	modules      TEXT NOT NULL DEFAULT '[]',
	updated_at   INTEGER NOT NULL
);

CREATE TABLE device_profiles (
	id                   INTEGER PRIMARY KEY,
	device_id            INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	sid                  TEXT NOT NULL,
	username             TEXT NOT NULL DEFAULT '',
	profile_path         TEXT NOT NULL DEFAULT '',
	vaultdock_user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	state                TEXT NOT NULL CHECK (state IN ('active_default','requested','authorised','blacklisted','cleared_pending_consent','revoked')),
	consent_id           TEXT NOT NULL DEFAULT '',
	consent_at           INTEGER,
	proof_digest         TEXT NOT NULL DEFAULT '',
	requested_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
	request_scope        TEXT NOT NULL DEFAULT '' CHECK (request_scope IN ('','selected','all')),
	blacklisted_at       INTEGER,
	blacklist_reason     TEXT NOT NULL DEFAULT '' CHECK (blacklist_reason IN ('','password_changed','password_reset','sid_changed','account_disabled','account_removed','profile_moved','consent_unverifiable')),
	blacklist_evidence   TEXT NOT NULL DEFAULT '',
	cleared_by           INTEGER REFERENCES users(id) ON DELETE SET NULL,
	cleared_at           INTEGER,
	created_at           INTEGER NOT NULL,
	updated_at           INTEGER NOT NULL,
	UNIQUE (device_id, sid)
);
CREATE INDEX device_profiles_state ON device_profiles(state);
CREATE INDEX device_profiles_user ON device_profiles(vaultdock_user_id);

CREATE TABLE profile_requests (
	id                   INTEGER PRIMARY KEY,
	device_id            INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	requested_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
	scope                TEXT NOT NULL CHECK (scope IN ('selected','all')),
	sids                 TEXT NOT NULL DEFAULT '[]',
	task_id              INTEGER REFERENCES device_tasks(id) ON DELETE SET NULL,
	status               TEXT NOT NULL DEFAULT 'pending',
	created_at           INTEGER NOT NULL,
	updated_at           INTEGER NOT NULL
);
CREATE INDEX profile_requests_device ON profile_requests(device_id, id);
`,
	// 9: restore progress and items (api-v1 rev 1.1c, Phase 2)
	`
ALTER TABLE restores ADD COLUMN conflict_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE restores ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE restores ADD COLUMN files_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE restores ADD COLUMN files_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE restores ADD COLUMN bytes_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE restores ADD COLUMN bytes_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE restore_items ADD COLUMN target_path TEXT NOT NULL DEFAULT '';
ALTER TABLE restore_items ADD COLUMN size INTEGER NOT NULL DEFAULT 0;
CREATE INDEX restore_items_order ON restore_items(restore_id, id);
`,
	// 10: storage nodes (vad-node agents on other machines)
	`
CREATE TABLE storage_nodes (
	id               INTEGER PRIMARY KEY,
	name             TEXT NOT NULL,
	status           TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','revoked')),
	hostname         TEXT NOT NULL DEFAULT '',
	reported_ip      TEXT NOT NULL DEFAULT '',
	request_ip       TEXT NOT NULL DEFAULT '',
	request_location TEXT NOT NULL DEFAULT '',
	os               TEXT NOT NULL DEFAULT '',
	arch             TEXT NOT NULL DEFAULT '',
	port             INTEGER NOT NULL DEFAULT 0,
	proxy            TEXT NOT NULL DEFAULT '',
	public_url       TEXT NOT NULL DEFAULT '',
	version          TEXT NOT NULL DEFAULT '',
	poll_token_hash  TEXT NOT NULL DEFAULT '',
	secret_hash      TEXT NOT NULL DEFAULT '',
	secret_delivered INTEGER NOT NULL DEFAULT 0,
	auto_update      INTEGER NOT NULL DEFAULT 1,
	update_requested INTEGER NOT NULL DEFAULT 0,
	disk_total       INTEGER NOT NULL DEFAULT 0,
	disk_free        INTEGER NOT NULL DEFAULT 0,
	uptime_seconds   INTEGER NOT NULL DEFAULT 0,
	last_ip          TEXT NOT NULL DEFAULT '',
	last_seen_at     INTEGER,
	last_error       TEXT NOT NULL DEFAULT '',
	terminal_enabled INTEGER NOT NULL DEFAULT 0,
	storage_roots    TEXT NOT NULL DEFAULT '',
	copy_key         TEXT NOT NULL DEFAULT '', -- sealed per-node key for copies (never sent to the node)
	decided_by       INTEGER REFERENCES users(id) ON DELETE SET NULL,
	decided_at       INTEGER,
	expires_at       INTEGER NOT NULL,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
);
CREATE INDEX storage_nodes_status ON storage_nodes(status);

-- Folders on a node that hold copies (never primary storage).
CREATE TABLE node_locations (
	id         INTEGER PRIMARY KEY,
	node_id    INTEGER NOT NULL REFERENCES storage_nodes(id) ON DELETE CASCADE,
	name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	path       TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'preparing' CHECK (status IN ('preparing','ok','error')),
	error      TEXT NOT NULL DEFAULT '',
	disk_total INTEGER NOT NULL DEFAULT 0,
	disk_free  INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (node_id, path)
);

-- "Files matching X stored in source get a copy on target".
-- target: 'local:<storage_locations.id>' or 'node:<node_locations.id>'.
CREATE TABLE copy_rules (
	id                 INTEGER PRIMARY KEY,
	name               TEXT NOT NULL,
	enabled            INTEGER NOT NULL DEFAULT 1,
	user_id            INTEGER REFERENCES users(id) ON DELETE CASCADE,
	source_location_id INTEGER REFERENCES storage_locations(id) ON DELETE CASCADE,
	target             TEXT NOT NULL,
	path_pattern       TEXT NOT NULL DEFAULT '',
	extensions         TEXT NOT NULL DEFAULT '',
	min_size           INTEGER NOT NULL DEFAULT 0,
	max_size           INTEGER NOT NULL DEFAULT 0,
	delete_delay_hours INTEGER NOT NULL DEFAULT 168,
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL
);

-- One row per stored copy of a blob on a target.
CREATE TABLE blob_copies (
	user_id            INTEGER NOT NULL,
	sha256             TEXT NOT NULL,
	target             TEXT NOT NULL,
	state              TEXT NOT NULL CHECK (state IN ('pending','ok','failed','deleting')),
	size               INTEGER NOT NULL DEFAULT 0,
	stored_sha256      TEXT NOT NULL DEFAULT '',
	last_path          TEXT NOT NULL DEFAULT '',
	delete_delay_hours INTEGER NOT NULL DEFAULT 168,
	attempts           INTEGER NOT NULL DEFAULT 0,
	next_at            INTEGER NOT NULL DEFAULT 0,
	delete_at          INTEGER,
	verified_at        INTEGER,
	last_error         TEXT NOT NULL DEFAULT '',
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL,
	PRIMARY KEY (user_id, sha256, target)
);
CREATE INDEX blob_copies_state ON blob_copies(state, next_at);
CREATE INDEX blob_copies_target ON blob_copies(target, state);

-- Integrity checks of the primary copy of blobs that have copies.
CREATE TABLE blob_checks (
	user_id     INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	verified_at INTEGER NOT NULL,
	ok          INTEGER NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, sha256)
);

-- Work for nodes, which pull it (long poll) so they need no inbound access.
CREATE TABLE node_jobs (
	id          INTEGER PRIMARY KEY,
	node_id     INTEGER NOT NULL REFERENCES storage_nodes(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	payload     TEXT NOT NULL DEFAULT '{}',
	status      TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','claimed','done','failed')),
	result      TEXT NOT NULL DEFAULT '{}',
	error       TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	claimed_at  INTEGER,
	finished_at INTEGER
);
CREATE INDEX node_jobs_queue ON node_jobs(node_id, status, id);
`,
	// 11: dismissed sync failure warnings
	`
ALTER TABLE users ADD COLUMN failures_dismissed_id INTEGER NOT NULL DEFAULT 0;
`,
	// 12: uploads the client compressed itself; admin reset of a device's
	// direct (LAN) upload setting
	`
ALTER TABLE uploads ADD COLUMN client_compressed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE devices ADD COLUMN direct_reset_gen INTEGER NOT NULL DEFAULT 0;
`,
}

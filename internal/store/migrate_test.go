package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// newTables lists every table added by migrations 2-8.
var newTables = []string{
	// 2
	"secrets", "destinations", "approvals", "operations", "plans",
	// 3
	"repositories", "repo_objects", "snapshots", "snapshot_packs", "snapshot_files", "snapshot_file_tokens", "catalog_keys",
	// 4
	"backup_jobs", "job_runs", "run_issues", "device_tasks", "app_inventory", "restores", "restore_items", "device_backup_state",
	// 5
	"retention_policies", "prune_plans", "repo_locks", "schedules",
	// 6
	"file_versions", "sync_folders", "sync_conflicts", "sync_changes",
	// 7
	"replication_policies", "replication_state", "notification_prefs", "ai_settings", "ai_audit",
	// 8
	"server_keys", "key_audit", "repo_keys", "device_keys", "device_modules", "device_profiles", "profile_requests",
	// 10
	"storage_nodes", "node_locations", "copy_rules", "blob_copies", "blob_checks", "node_jobs",
	// 13
	"video_drafts", "video_exports", "reupload_blocks",
	// 15
	"smart_presets", "smart_queues", "smart_items", "smart_segments", "smart_outputs", "smart_errors",
	// 16
	"api_keys",
}

// newColumns lists columns added to pre-existing tables.
var newColumns = map[string][]string{
	"devices":       {"can_delete"},
	"users":         {"is_superuser", "backup_quota_bytes", "failures_dismissed_id"},
	"nodes":         {"revision", "trash_expires_at", "file_created_at"},
	"uploads":       {"folder_id", "base_revision", "base_sha256", "previous_path", "file_created_at"},
	"automations":   {"schema_version", "runtime"},
	"restores":      {"conflict_policy", "cancel_requested", "files_total", "files_done", "bytes_total", "bytes_done"},
	"restore_items": {"target_path", "size"},
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func assertSchema(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != 16 || LatestSchemaVersion() != 16 {
		t.Fatalf("schema version = %d, latest = %d, want 16", v, LatestSchemaVersion())
	}
	for _, tbl := range newTables {
		var n int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
	for tbl, cols := range newColumns {
		for _, col := range cols {
			var n int
			if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", tbl, col).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("column %s.%s missing", tbl, col)
			}
		}
	}
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Fatalf("integrity: %v", err)
	}
	rows, err := s.DB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported violations")
	}
}

func TestMigrateFresh(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "fresh.db"))
	assertSchema(t, s)

	// The first admin created on a fresh install becomes the superuser; later
	// admins do not.
	for i, role := range []string{"user", "admin", "admin"} {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO users(username, password_hash, role, created_at, updated_at) VALUES (?, 'x', ?, ?, ?)`,
			fmt.Sprintf("u%d", i), role, i, i); err != nil {
			t.Fatal(err)
		}
	}
	got := superusers(t, s.DB)
	if len(got) != 1 || got[0] != "u1" {
		t.Fatalf("superusers = %v, want [u1]", got)
	}

	// Reopening is a no-op.
	s.Close()
	s2 := openTestStore(t, s.Path)
	assertSchema(t, s2)
}

func TestMigrateFromV1(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")

	// Build a v1 database with data, exactly as a released build would.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		migrations[0],
		"PRAGMA user_version = 1",
		`INSERT INTO users(id, username, password_hash, role, created_at, updated_at) VALUES
			(1, 'alice', 'x', 'user', 100, 100),
			(2, 'bob', 'x', 'admin', 300, 300),
			(3, 'carol', 'x', 'admin', 200, 200),
			(4, 'dave', 'x', 'admin', 200, 200)`,
		`INSERT INTO devices(id, user_id, name, identifier, public_key, key_fingerprint, status, user_code, created_at, expires_at) VALUES
			(1, 1, 'pc1', 'id1', 'pk1', 'fp1', 'approved', 'C1', 1, 2),
			(2, 2, 'pc2', 'id2', 'pk2', 'fp2', 'pending', 'C2', 1, 2)`,
		`INSERT INTO nodes(id, user_id, parent_id, name, is_dir, created_at, modified_at, updated_at) VALUES
			(1, 1, NULL, 'docs', 1, 1, 1, 1),
			(2, 1, 1, 'a.txt', 0, 1, 1, 1)`,
		`INSERT INTO automations(id, user_id, name, definition, created_at, updated_at) VALUES (1, 1, 'auto', '{}', 1, 1)`,
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			db.Close()
			t.Fatalf("seed v1: %v", err)
		}
	}
	db.Close()

	s := openTestStore(t, path)
	assertSchema(t, s)

	// Earliest-created admin (carol: created_at 200, lower id than dave).
	if got := superusers(t, s.DB); len(got) != 1 || got[0] != "carol" {
		t.Fatalf("superusers = %v, want [carol]", got)
	}

	checks := []struct {
		name  string
		query string
		want  int64
	}{
		{"devices.can_delete default", "SELECT COUNT(*) FROM devices WHERE can_delete = 1", 2},
		{"nodes.revision default", "SELECT COUNT(*) FROM nodes WHERE revision = 1", 2},
		{"users.backup_quota_bytes default", "SELECT SUM(backup_quota_bytes) FROM users", 0},
		{"automations.schema_version default", "SELECT schema_version FROM automations WHERE id = 1", 1},
		{"automations.runtime default", "SELECT COUNT(*) FROM automations WHERE runtime = 'device'", 1},
		{"data kept: users", "SELECT COUNT(*) FROM users", 4},
	}
	for _, c := range checks {
		var got int64
		if err := s.DB.QueryRowContext(ctx, c.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s = %d, want %d", c.name, got, c.want)
		}
	}

	// A new admin after migration must not become superuser.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO users(username, password_hash, role, created_at, updated_at) VALUES ('erin', 'x', 'admin', 400, 400)`); err != nil {
		t.Fatal(err)
	}
	if got := superusers(t, s.DB); len(got) != 1 {
		t.Fatalf("superusers after new admin = %v", got)
	}

	// Smoke-test the new schema's main relations with the migrated rows.
	smoke := []string{
		`INSERT INTO destinations(id, scope, user_id, name, kind, created_at, updated_at) VALUES (1, 'user', 1, 'Server', 'server', 1, 1)`,
		`INSERT INTO repositories(id, user_id, destination_id, repo_uuid, name, created_by_device_id, created_at, updated_at) VALUES (1, 1, 1, 'u-1', 'r', 1, 1, 1)`,
		`INSERT INTO repo_objects(id, repo_id, kind, oid, size, created_at) VALUES (1, 1, 'data', 'aa', 10, 1)`,
		`INSERT INTO snapshots(id, repo_id, user_id, device_id, snapshot_oid, committed_at) VALUES (1, 1, 1, 1, 'bb', 1)`,
		`INSERT INTO snapshot_packs(snapshot_id, object_id) VALUES (1, 1)`,
		`INSERT INTO backup_jobs(id, user_id, device_id, repo_id, name, kind, created_at, updated_at) VALUES (1, 1, 1, 1, 'j', 'folder', 1, 1)`,
		`INSERT INTO sync_folders(id, user_id, device_id, local_path, remote_path, created_at, updated_at) VALUES (1, 1, 1, 'C:\Docs', '/docs', 1, 1)`,
		`INSERT INTO sync_changes(user_id, seq, node_id, op, path, revision, modified_at, created_at) VALUES (1, 1, 2, 'create', '/docs/a.txt', 1, 1, 1)`,
		`INSERT INTO device_profiles(device_id, sid, state, created_at, updated_at) VALUES (1, 'S-1-5-21-1', 'active_default', 1, 1)`,
		`INSERT INTO server_keys(kind, version, public, private_sealed_by_kek, created_at) VALUES ('sik', 1, x'01', x'02', 1)`,
	}
	for _, q := range smoke {
		if _, err := s.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var dir, state, escrow string
	var epoch, rev int
	if err := s.DB.QueryRowContext(ctx, "SELECT direction, state, revision FROM sync_folders WHERE id = 1").Scan(&dir, &state, &rev); err != nil {
		t.Fatal(err)
	}
	if dir != "upload" || state != "active" || rev != 1 {
		t.Errorf("sync_folders defaults = %s/%s/%d", dir, state, rev)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT escrow_mode, current_epoch FROM repositories WHERE id = 1").Scan(&escrow, &epoch); err != nil {
		t.Fatal(err)
	}
	if escrow != "full" || epoch != 1 {
		t.Errorf("repositories defaults = %s/%d", escrow, epoch)
	}
	// Only one current key per kind.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO server_keys(kind, version, public, private_sealed_by_kek, created_at) VALUES ('sik', 2, x'01', x'02', 1)`); err == nil {
		t.Error("second unretired sik accepted")
	}
	// Bad enum values are rejected.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sync_folders(user_id, device_id, local_path, remote_path, direction, created_at, updated_at) VALUES (1, 1, 'D:\x', '/x', 'download', 1, 1)`); err == nil {
		t.Error("invalid sync direction accepted")
	}
}

func superusers(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT username FROM users WHERE is_superuser = 1 ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

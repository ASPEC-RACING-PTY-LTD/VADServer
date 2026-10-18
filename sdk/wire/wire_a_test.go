package wire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// wireADecodeStrict decodes like a server handler: unknown fields are rejected.
func wireADecodeStrict(data string, v any) error {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// canonical re-encodes JSON through a generic value so key order and spacing
// do not matter in comparisons.
func wireACanonical(t *testing.T, data []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("canonical: %v", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	return string(out)
}

func TestWireARoundTrip(t *testing.T) {
	tests := []struct {
		name string
		json string
		new  func() any
	}{
		{"capabilities §2", `{"formats":["vaultdock.repo/1"],"max_object_bytes":67108864,"pack_target_bytes":16777216,
			"max_missing_batch":20000,"max_commit_bytes":67108864,"escrow_modes":["full","metadata"],
			"default_escrow_mode":"full","long_poll_seconds":25,"modules_supported":["core","sync","automation","vss","discovery","reinstall","profiles"]}`,
			func() any { return &Capabilities{} }},
		{"summary §3", `{"pending_devices":1,"pending_approvals":2,"running_jobs":3,"failed_runs_24h":4,
			"unread_notifications":5,"open_conflicts":6,"blacklisted_profiles":7,"readiness":"warning"}`,
			func() any { return &Summary{} }},
		{"operation running §3", `{"id":1,"kind":"snapshot_download","state":"running",
			"progress":{"done":1,"total":4,"bytes":1024},"result":{},"error":"","created_at":1,"updated_at":2}`,
			func() any { return &Operation{} }},
		{"operation failed §3", `{"id":2,"kind":"gc","state":"failed","progress":{"done":0,"total":0,"bytes":0},
			"result":{"download_url":"https://x/y"},"error":"pack missing",
			"created_at":1,"updated_at":2}`,
			func() any { return &Operation{} }},
		{"destination s3 §4", `{"id":4,"scope":"user","name":"R2","kind":"s3",
			"config":{"path":"","endpoint":"https://r2","bucket":"b","prefix":"p/","region":"auto","provider":"r2","path_style":true},
			"has_secret":true,"secret_updated_at":1700000000000,"enabled":true,"health":"ok","last_checked_at":5,
			"capabilities":{"object_lock":true,"conditional_put":true,"server_side_copy":false},"created_at":1,"updated_at":2}`,
			func() any { return &Destination{} }},
		{"destination local §4", `{"id":5,"scope":"global","name":"NAS","kind":"local","config":{"path":"/mnt/nas","endpoint":"","bucket":"","prefix":"","region":"","provider":"","path_style":false},
			"has_secret":false,"secret_updated_at":0,"enabled":false,"health":"unknown","last_checked_at":0,
			"capabilities":{"object_lock":false,"conditional_put":true,"server_side_copy":true},"created_at":1,"updated_at":2}`,
			func() any { return &Destination{} }},
		{"destination create set secret §4", `{"name":"R2","kind":"s3","config":{"path":"","endpoint":"e","bucket":"b","prefix":"","region":"auto","provider":"r2","path_style":false},"secret":"k","enabled":true}`,
			func() any { return &DestinationCreateRequest{} }},
		{"destination update clear secret §4", `{"name":"n","secret":null}`,
			func() any { return &DestinationUpdateRequest{} }},
		{"destination update keep secret §4", `{"enabled":false}`,
			func() any { return &DestinationUpdateRequest{} }},
		{"destination ref §4", `{"id":1,"name":"Server"}`, func() any { return &DestinationRef{} }},
		{"enrol request §5A", `{"login":"a","password":"p","device_name":"PC","os":"windows","identifier":"i",
			"public_key":"pk","client_version":"1.0","box_pub":"bx","install_mode":"simple","core_version":"2.0"}`,
			func() any { return &EnrolRequest{} }},
		{"enrol status approved §5A", `{"status":"approved","device_id":3,"expires_at":9,
			"server_identity":{"sik_pub":"s","sik_fingerprint":"f","sek_pub":"e","sek_version":2,"sek_sig":"g"}}`,
			func() any { return &EnrolStatus{} }},
		{"enrol status pending §5A", `{"status":"pending","expires_at":9}`, func() any { return &EnrolStatus{} }},
		{"keys server §5A", `{"server_identity":{"sik_pub":"s","sik_fingerprint":"f","sek_pub":"e","sek_version":1,"sek_sig":"g"}}`,
			func() any { return &ServerIdentityResponse{} }},
		{"escrow change §5A", `{"escrow_mode":"metadata"}`, func() any { return &EscrowChangeRequest{} }},
		{"keys status §5A", `{"current_epoch":3,"recipients":[{"kind":"device","label":"DESKTOP-ABC","epoch":3,"device_id":7},
			{"kind":"recovery","label":"recovery","epoch":3}],"recovery_wrapped_current":true,"escrow_mode":"full"}`,
			func() any { return &KeyStatus{} }},
		{"repo §5", `{"id":1,"repo_uuid":"u","name":"r","destination_id":2,"format_version":1,"escrow_mode":"full",
			"current_epoch":1,"created_by_device_id":3,"created_at":4,
			"usage":{"objects":5,"stored_bytes":6,"logical_bytes":7,"snapshots":8},"last_catalog_sync_at":9}`,
			func() any { return &Repo{} }},
		{"repo list §5", `{"items":[{"id":1,"repo_uuid":"u","name":"r","destination_id":2,"format_version":1,"escrow_mode":"metadata",
			"current_epoch":1,"created_by_device_id":3,"created_at":4,
			"usage":{"objects":0,"stored_bytes":0,"logical_bytes":0,"snapshots":0},"last_catalog_sync_at":0}],"next_cursor":"c"}`,
			func() any { return &List[Repo]{} }},
		{"repo create §5", `{"repo_uuid":"u","name":"r","destination_id":2,"escrow_mode":"full","format_version":1}`,
			func() any { return &RepoCreateRequest{} }},
		{"repo create default escrow §5", `{"repo_uuid":"u","name":"r","destination_id":2,"format_version":1}`,
			func() any { return &RepoCreateRequest{} }},
		{"objects missing §5", `{"kind":"data","oids":["aa","bb"]}`, func() any { return &ObjectMissingRequest{} }},
		{"objects missing reply §5", `{"missing":["bb"]}`, func() any { return &ObjectMissingResponse{} }},
		{"lock request §5", `{"kind":"exclusive","ttl_seconds":300}`, func() any { return &LockRequest{} }},
		{"lock §5", `{"lock_id":"l-4","expires_at":10}`, func() any { return &Lock{} }},
		{"snapshot commit §5.2", `{"snapshot_oid":"s","job_id":1,"run_id":2,"parent_snapshot_id":null,"kind":"folder",
			"mode":"full","hostname":"PC","os_user":"ali","user_sid":"S-1-5-21-1","profile_path":"C:\\Users\\ali",
			"client_time":5,"tags":["daily"],"stats":{"file_count":10,"dir_count":2,"logical_bytes":100,"new_bytes":50,"new_objects":3,"duration_ms":900},
			"index_oids":["i"],"pack_oids":["p1","p2"],"epoch":2,"profile_consent_id":"c"}`,
			func() any { return &SnapshotCommitRequest{} }},
		{"snapshot commit reply §5.2", `{"snapshot_id":42}`, func() any { return &SnapshotCommitResponse{} }},
		{"snapshot §5.4", `{"id":42,"repo_id":1,"snapshot_oid":"s","job_id":1,"device_id":3,"parent_id":41,
			"kind":"folder","mode":"full","hostname":"PC","os_user":"ali","profile_path":"C:\\Users\\ali","client_time":5,
			"committed_at":6,"tags":[],"stats":{"file_count":10,"dir_count":1,"logical_bytes":100,"new_bytes":50,"new_objects":1,"duration_ms":7},"protected":true,
			"protected_until":null,"status":"committed","epoch":2,"profile":{"sid":"S-1-5-21-1","username":"ali"},"ingest_state":"done","browsable":true}`,
			func() any { return &Snapshot{} }},
		{"catalog tree §5.4", `{"items":[{"path":"{DOCUMENTS}/a.txt","name":"a.txt","type":"file","size":3,"modified_at":4}],"next_cursor":""}`,
			func() any { return &List[CatalogEntry]{} }},
		{"catalog diff §5.4", `{"added":0,"removed":0,"changed":1,"paths":[{"path":"{DOCUMENTS}/a.txt","type":"file","change":"changed","old_size":3,"new_size":4}]}`,
			func() any { return &SnapshotDiff{} }},
		{"objects list §5", `{"items":[{"oid":"aa","size":3}],"next_cursor":""}`, func() any { return &List[ObjectInfo]{} }},
		{"destination test result §4", `{"ok":false,"checks":[{"name":"write","ok":false,"message":"denied"}]}`,
			func() any { return &DestinationTestResult{} }},
		{"destination providers §4", `[{"kind":"s3","provider":"r2","fields":[{"name":"secret","label":"Secret key","type":"password","required":true,"secret":true}]}]`,
			func() any { return &[]DestinationProvider{} }},
		{"operation accepted §1", `{"operation_id":7}`, func() any { return &OperationAccepted{} }},
		{"approval accepted §1", `{"approval_id":3,"operation_id":null}`, func() any { return &ApprovalAccepted{} }},
		{"snapshot patch §5.4", `{"tags":["keep"],"protected":false}`, func() any { return &SnapshotPatchRequest{} }},
		{"snapshot download §5.4", `{"paths":["{DOCUMENTS}/a.txt"]}`, func() any { return &SnapshotDownloadRequest{} }},
		{"snapshot download result §5.4", `{"download_url":"https://x/dl/t"}`, func() any { return &SnapshotDownloadResult{} }},
		{"catalog rebuild §5.4", `{"repo_id":1}`, func() any { return &CatalogRebuildRequest{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.new()
			if err := wireADecodeStrict(tc.json, v); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got, want := wireACanonical(t, out), wireACanonical(t, []byte(tc.json)); got != want {
				t.Fatalf("round trip mismatch\n got: %s\nwant: %s", got, want)
			}
			again := tc.new()
			if err := wireADecodeStrict(string(out), again); err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if !reflect.DeepEqual(v, again) {
				t.Fatalf("re-decoded value differs: %#v vs %#v", v, again)
			}
		})
	}
}

func TestWireARejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		json string
		v    any
	}{
		{"destination create", `{"name":"n","kind":"local","config":{"path":"/x"},"credential_scope":"client_only"}`, &DestinationCreateRequest{}},
		{"snapshot run_id", `{"id":1,"run_id":2}`, &Snapshot{}},
		{"lock reply", `{"lock_id":"l","kind":"shared","expires_at":1}`, &Lock{}},
		{"destination config", `{"name":"n","kind":"s3","config":{"bucket":"b","access_key":"k"}}`, &DestinationCreateRequest{}},
		{"destination update", `{"scope":"global"}`, &DestinationUpdateRequest{}},
		{"enrol", `{"login":"a","box_pub":"b","encryption_mode":"client"}`, &EnrolRequest{}},
		{"escrow change", `{"escrow_mode":"full","force":true}`, &EscrowChangeRequest{}},
		{"repo create", `{"repo_uuid":"u","name":"r","destination_id":1,"format_version":1,"encryption_mode":"client"}`, &RepoCreateRequest{}},
		{"objects missing", `{"kind":"data","oids":[],"limit":1}`, &ObjectMissingRequest{}},
		{"lock", `{"kind":"shared","ttl_seconds":60,"holder":"x"}`, &LockRequest{}},
		{"snapshot commit", `{"snapshot_oid":"s","listing":[]}`, &SnapshotCommitRequest{}},
		{"snapshot commit stats", `{"snapshot_oid":"s","stats":{"file_count":1,"dirs":2}}`, &SnapshotCommitRequest{}},
		{"snapshot patch", `{"protected":false,"reason":"x"}`, &SnapshotPatchRequest{}},
		{"snapshot download", `{"paths":[],"format":"tar"}`, &SnapshotDownloadRequest{}},
		{"catalog rebuild", `{"repo_id":1,"all":true}`, &CatalogRebuildRequest{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := wireADecodeStrict(tc.json, tc.v)
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("want unknown field error, got %v", err)
			}
		})
	}
}

func TestWireADestinationSecretTriState(t *testing.T) {
	cases := []struct {
		json    string
		present bool
		value   *string
	}{
		{`{}`, false, nil},
		{`{"secret":null}`, true, nil},
		{`{"secret":"k"}`, true, wireAPtr("k")},
	}
	for _, tc := range cases {
		var req DestinationUpdateRequest
		if err := wireADecodeStrict(tc.json, &req); err != nil {
			t.Fatalf("%s: %v", tc.json, err)
		}
		if req.Secret.Present != tc.present || !reflect.DeepEqual(req.Secret.Value, tc.value) {
			t.Fatalf("%s: got %+v", tc.json, req.Secret)
		}
	}
}

func TestWireANullableAndOptionalFields(t *testing.T) {
	// Nullable fields are always written, as null when unset.
	out, err := json.Marshal(SnapshotCommitRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"parent_snapshot_id":null`)) {
		t.Fatalf("parent_snapshot_id not null: %s", out)
	}
	out, _ = json.Marshal(Operation{})
	for _, want := range []string{`"id":0`, `"error":""`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	// Optional fields are omitted when unset.
	out, _ = json.Marshal(RepoCreateRequest{RepoUUID: "u", FormatVersion: RepoFormatVersion})
	if bytes.Contains(out, []byte("escrow_mode")) {
		t.Fatalf("escrow_mode not omitted: %s", out)
	}
	out, _ = json.Marshal(EnrolStatus{Status: EnrolStatusPending})
	if bytes.Contains(out, []byte("server_identity")) || bytes.Contains(out, []byte("device_id")) {
		t.Fatalf("pending enrol status leaks approval fields: %s", out)
	}
	if len(ObjectConfigOID) != 64 || strings.Trim(ObjectConfigOID, "0") != "" {
		t.Fatalf("bad config oid %q", ObjectConfigOID)
	}
}

func wireAPtr[T any](v T) *T { return &v }

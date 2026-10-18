package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// strictDecode decodes like a server handler: unknown fields are rejected.
func strictDecode(t *testing.T, data []byte, v any) error {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func TestWireBRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		json string
		v    any
	}{
		{"job", `{"id":1,"device_id":3,"repo_id":4,"name":"Docs","kind":"folder","mode":"full","spec":{"paths":["{DOCUMENTS}"],"exclude":["**/node_modules"],"ruleset_id":null,"source_ref":"","use_vss":"required"},"schedule":{"type":"weekly","expr":"","at":"02:00","weekdays":[1,2,3,4,5],"every_minutes":0,"timezone":"Local","catch_up":"run_once","jitter_minutes":10},"retention_policy_id":null,"priority":0,"enabled":true,"revision":7,"profile_sid":"S-1","required_modules":["vss"],"state":"suspended_by_mode","created_at":1,"updated_at":2}`, &Job{}},
		{"job_update", `{"enabled":false,"profile_sid":"S-1","schedule":{"type":"manual","expr":"","at":"","weekdays":[],"every_minutes":0,"timezone":"Local","catch_up":"skip","jitter_minutes":0}}`, &JobUpdateRequest{}},
		{"run", `{"id":1,"job_id":2,"device_id":3,"client_run_uuid":"u","trigger":"manual","snapshot_id":9,"started_at":1,"finished_at":2,"status":"partial","phase":"commit","files_total":3,"files_done":3,"bytes_total":4,"bytes_done":4,"bytes_new":1,"bytes_uploaded":1,"queue_packs":0,"warnings":1,"errors":0,"eta_ms":0}`, &Run{}},
		{"run_start", `{"client_run_uuid":"u","trigger":"schedule"}`, &RunStartRequest{}},
		{"run_progress", `{"status":"uploading","phase":"upload","files_total":3,"files_done":1,"bytes_total":4,"bytes_done":2,"bytes_new":1,"bytes_uploaded":1,"queue_packs":2,"warnings":0,"errors":0,"eta_ms":5000}`, &RunProgress{}},
		{"issue", `{"id":1,"run_id":345,"path":"{APPDATA}/x.db","class":"locked","severity":"skipped","detail":"d","first_seen":1,"last_seen":2,"count":3,"retryable":true,"resolution":"open","retry_run_id":null}`, &Issue{}},
		{"issues", `{"issues":[{"path":"C:\\x","class":"guard_blocked","severity":"skipped","detail":"m","first_seen":1,"last_seen":1,"count":1,"retryable":false}]}`, &IssueBatchRequest{}},
		{"task", `{"id":7,"device_id":3,"kind":"rewrap","payload":{"repo_id":4,"epoch":2},"status":"pending","approval_id":null,"required_modules":["core"],"created_by":"server","created_at":10,"claimed_at":0,"finished_at":0,"result":{}}`, &DeviceTask{}},
		{"task_poll", `{"items":[]}`, &DeviceTaskPollResponse{}},
		{"task_created", `{"task_id":5,"approval_id":6}`, &DeviceTaskCreated{}},
		{"task_result", `{"status":"failed","result":{"error":"no vss"}}`, &DeviceTaskResultRequest{}},
		{"inventory", `{"schema":"inventory/1","apps":[{"name":"Git","version":"2.50","publisher":"Git","source":"winget","package_id":"Git.Git","match_method":"winget_list","tier":1,"install_scope":"machine","mode":"reinstall_inventory","install_location":"C:\\Program Files\\Git","uninstall_key":"k","flags":["settings_before_install"],"data_roots":["{APPDATA}/Git"],"registry":[]}],"games":[{"name":"G","store":"steam","app_id":"1","save_roots":["{DOCUMENTS}/G"],"manifest_source":"builtin"}],"devprojects":[{"root":"{PROFILE}/src/x","remote":"r","branch":"main","head":"abc","dirty":false,"pushed":true,"mode":"data_only"}],"not_portable":[{"item":"x","reason":"y"}]}`, &Inventory{}},
		{"device_apps", `{"items":[{"name":"Git","version":"2.50","publisher":"Git","source":"winget","package_id":"Git.Git","match_method":"winget_list","tier":1,"install_scope":"machine","mode":"full","data_bytes":10,"last_snapshot_id":null,"flags":["not_portable"]}],"next_cursor":""}`, &List[DeviceApp]{}},
		{"modules", `{"install_mode":"advanced","core_version":"2.0.0","modules":[{"id":"vss","version":"1.0.0","state":"failed","error":"boom"},{"id":"core","version":"2.0.0","state":"enabled"}]}`, &ModuleReport{}},
		{"catalog", `[{"id":"sync","name":"Sync","summary":"s","modes":["advanced"],"delivery":"builtin","requires_features":["sync_two_way.v1"]}]`, &[]ModuleCatalogEntry{}},
		{"profile", `{"id":1,"device_id":2,"sid":"S-1","username":"a","profile_path":"C:\\Users\\a","vaultdock_user_id":null,"state":"blacklisted","consent_id":"c1","consent_at":5,"requested_by_user_id":null,"request_scope":"all","blacklisted_at":6,"blacklist_reason":"password_reset","cleared_by":null,"cleared_at":null,"updated_at":7}`, &Profile{}},
		{"profile_report", `{"profiles":[{"sid":"S-1","username":"a","profile_path":"C:\\Users\\a","state":"active_default"}],"consents":[{"consent_id":"c1","sid":"S-1","consent_at":1,"proof_digest":"ab"}]}`, &ProfileReport{}},
		{"security_event", `{"signal":"password_changed","observed_at":1,"evidence":"eventlog_4723"}`, &ProfileSecurityEventRequest{}},
		{"restore_plan_req", `{"snapshot_id":1,"selection":[{"path":"{DOCUMENTS}","include":true},{"path":"{DOCUMENTS}/tmp","include":false}],"target_device_id":2,"target_user":{"sid":"S-1"},"target_profile_sid":"S-1","mapping":{"auto":true,"overrides":[{"from":"C:\\Users\\a","to":"D:\\a"}]},"conflict_policy":"rename","reinstall_apps":["Git.Git"],"include_registry":false}`, &RestorePlanRequest{}},
		{"restore_plan", `{"summary":{"files":1,"bytes":1,"conflicts":0,"reinstalls":0},"mappings":[{"token":"{PROFILE}","from":"C:\\Users\\a","to":"C:\\Users\\b","auto":true}],"actions":[{"path":"{DOCUMENTS}/a","target_path":"C:\\Users\\b\\Documents\\a","action":"data_only","size":1}],"warnings":[{"code":"module_unavailable","message":"no reinstall"}],"not_portable":["App"],"guard":[{"path":"C:\\Windows\\x","reason":"windir"}]}`, &RestorePlan{}},
		{"restore_execute", `{"restore_id":4,"approval_id":5}`, &RestoreExecuteResponse{}},
		{"restore", `{"id":4,"snapshot_id":1,"target_device_id":2,"target_profile_sid":"S-1","approval_id":5,"task_id":null,"status":"awaiting_approval","files_total":0,"files_done":0,"bytes_total":0,"bytes_done":0,"created_at":1,"updated_at":1}`, &Restore{}},
		{"approval", `{"id":1,"kind":"prune","subject_type":"prune_plan","subject_id":9,"plan_digest":"ab","summary":"Delete 14 snapshots (38.2 GB)","risk":"high","requested_by":"automation","requester_ref":"a1","state":"pending","expires_at":9,"decided_by":null,"decided_at":null,"reason":""}`, &Approval{}},
		{"approval_reject", `{"reason":"no"}`, &ApprovalRejectRequest{}},
		{"retention", `{"id":1,"name":"std","rules":{"keep_last":0,"keep_hourly":0,"keep_daily":7,"keep_weekly":4,"keep_monthly":12,"keep_yearly":0,"keep_within_days":0,"keep_tags":[]},"created_at":1,"updated_at":2}`, &RetentionPolicy{}},
		{"prune_plan", `{"forget":[{"snapshot_id":2,"reason":"expired"}],"keep":[{"snapshot_id":3,"reasons":["keep_daily","protected"]}],"packs_to_delete":3,"bytes_reclaimed":4,"bytes_reclaimable_after_compaction":5}`, &PrunePlan{}},
		{"backup_settings", `{"grace_hours":24,"max_object_mb":64,"scrub_cron":"","db_backup_cron":"","default_retention_policy_id":null}`, &BackupSettings{}},
		{"verify", `{"depth":"sample","sample_pct":5}`, &VerifyRequest{}},
		{"replication", `{"id":1,"src_repo_id":2,"dst_destination_id":3,"mode":"mirror","schedule":null,"enabled":true,"status":{"lag_objects":0,"lag_bytes":0,"last_run_at":null,"state":"idle"},"created_at":1,"updated_at":2}`, &Replication{}},
		{"analytics", `{"logical_bytes":10,"stored_bytes":5,"dedup_ratio":2,"reclaimable_bytes":1,"series":[{"t":1,"logical":10,"stored":5}],"top":[{"key":"Documents","unique_bytes":3}]}`, &Analytics{}},
		{"sync_folder", `{"id":1,"device_id":2,"profile_sid":"S-1","local_path":"C:\\Users\\a\\Docs","remote_path":"/Docs","direction":"two_way","conflict_policy":"ask","delete_policy":"propagate_to_trash","mass_delete_threshold":{"files":50,"percent":10},"ruleset":{},"schedule":{},"enabled":true,"revision":3,"acked_revision":2,"state":"active","created_at":1,"updated_at":2}`, &SyncFolder{}},
		{"changes", `{"items":[{"seq":1,"node_id":2,"op":"move","path":"/b","old_path":"/a","revision":3,"sha256":"00","size":1,"modified_at":1,"device_id":4}],"next_since":1,"reset":false}`, &SyncChangesResponse{}},
		{"ops", `{"ops":[{"op":"delete","path":"/a","base_revision":2},{"op":"move","path":"/b","new_path":"/c","base_revision":1}],"confirm_mass_delete":false}`, &SyncOpsRequest{}},
		{"versions", `{"items":[{"id":1,"sha256":"00","size":1,"modified_at":1,"origin_device_id":null,"created_at":1,"pinned":true}],"next_cursor":""}`, &List[Version]{}},
		{"conflict_resolve", `{"ids":[1,2],"choice":"keep_both"}`, &ConflictResolveRequest{}},
		{"readiness", `{"score":80,"status":"warning","devices":[{"device_id":1,"checks":[{"id":"modules_healthy","status":"warning","message":"vss failed","fix_link":"/devices/1"}],"jobs":[{"job_id":2,"last_success_at":1,"open_issues":0,"vss_used":true,"verified_at":0,"test_restored_at":0}]}]}`, &ReadinessReport{}},
		{"backup_state", `{"recovery_key_confirmed":true,"service_installed":true,"vss_available":false}`, &BackupState{}},
		{"notif", `[{"event":"module.failed","channel":"webhook","enabled":true}]`, &[]NotificationPref{}},
		{"ai", `{"enabled":true,"base_url":"https://llm.example/v1","model":"m","scopes":["diagnostics"],"has_secret":true,"secret_updated_at":5}`, &AISettings{}},
		{"ai_diagnose", `{"run_id":3,"question":"why?"}`, &AIDiagnoseRequest{}},
		{"ai_diagnose_reply", `{"report_markdown":"# r","proposals":[{"kind":"retry","summary":"s","approval_id":4},{"kind":"note","summary":"t"}]}`, &AIDiagnoseResponse{}},
		{"deploy_env", `{"schema_version":3,"added_by_upgrade":[{"name":"A","default":"1","since":"2.0.0"}],"obsolete_present":[{"name":"B","deprecated_since":"2.0.0","replacement":"C","note":"n"}],"pending_removals":[{"name":"B","approved_by":1,"approved_at":2}]}`, &DeployEnvStatus{}},
		{"superuser", `{"is_superuser":true}`, &SuperuserUpdateRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := strictDecode(t, []byte(tc.json), tc.v); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var want, got any
			if err := json.Unmarshal([]byte(tc.json), &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip differs\nwant %s\ngot  %s", tc.json, out)
			}
		})
	}
}

func TestWireBRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		json string
		v    any
	}{
		{`{"device_id":1,"kind":"verify","payload":{},"bogus":1}`, &DeviceTaskCreateRequest{}},
		{`{"install_mode":"simple","core_version":"1","modules":[{"id":"core","version":"1","state":"enabled","x":1}]}`, &ModuleReport{}},
		{`{"signal":"sid_changed","observed_at":1,"evidence":"sid_lookup","password":"no"}`, &ProfileSecurityEventRequest{}},
		{`{"ops":[],"confirm_mass_delete":true,"force":true}`, &SyncOpsRequest{}},
		{`{"ids":[1],"choice":"keep_local","all":true}`, &ConflictResolveRequest{}},
		{`{"client_run_uuid":"u","trigger":"manual","epoch":1}`, &RunStartRequest{}},
		{`{"status":"done","result":{},"error":null}`, &DeviceTaskResultRequest{}},
		{`{"keep_last":1,"keep_within_ms":5}`, &RetentionRules{}},
		{`{"depth":"hash","snapshot_id":2}`, &VerifyRequest{}},
		{`{"names":["A"],"confirm_text":"REMOVE 1 VARIABLES","value":"x"}`, &DeployEnvRemovalRequest{}},
		{`{"enabled":true,"base_url":"https://x","model":"m","scopes":[],"api_key":"k"}`, &AISettingsUpdate{}},
	}
	for _, tc := range cases {
		if err := strictDecode(t, []byte(tc.json), tc.v); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%T: want unknown field error, got %v", tc.v, err)
		}
	}
}

func TestDecodeTaskPayload(t *testing.T) {
	cases := []struct {
		kind    string
		payload string
		want    any
	}{
		{TaskKindBackupNow, `{"job_id":3}`, &TaskBackupNowPayload{JobID: 3}},
		{TaskKindRestore, `{"restore_id":8}`, &TaskRestorePayload{RestoreID: 8}},
		{TaskKindRetrySkipped, `{"run_id":2}`, &TaskRetrySkippedPayload{RunID: 2}},
		{TaskKindVerify, `{"repo_id":1,"depth":"sample","sample_pct":5}`, &TaskVerifyPayload{RepoID: 1, Depth: TaskVerifyDepthSample, SamplePct: 5}},
		{TaskKindTestRestore, `{"snapshot_id":4,"sample_files":20}`, &TaskTestRestorePayload{SnapshotID: 4, SampleFiles: 20}},
		{TaskKindAutomationOp, `{"automation_id":1,"node_id":"n1","op":{"kind":"backup","params":{"job_id":2}}}`, &TaskAutomationOpPayload{AutomationID: 1, NodeID: "n1", Op: TaskAutomation{Kind: "backup", Params: json.RawMessage(`{"job_id":2}`)}}},
		{TaskKindKeyShare, `{"repo_id":1,"key_file_oid":"ab"}`, &TaskKeySharePayload{RepoID: 1, KeyFileOID: "ab"}},
		{TaskKindKeyShare, `{"repo_id":1,"target_device_id":2,"target_box_pub":"cHVi"}`, &TaskKeySharePayload{RepoID: 1, TargetDeviceID: 2, TargetBoxPub: "cHVi"}},
		{TaskKindRewrap, `{"repo_id":1,"epoch":4}`, &TaskRewrapPayload{RepoID: 1, Epoch: 4}},
		{TaskKindProfileConsentRequest, `{"request_id":"r","sids":"all","requested_by":"admin"}`, &TaskProfileConsentRequestPayload{RequestID: "r", SIDs: TaskSIDs{All: true}, RequestedBy: "admin"}},
		{TaskKindProfileConsentRequest, `{"request_id":"r","sids":["S-1"],"requested_by":"admin"}`, &TaskProfileConsentRequestPayload{RequestID: "r", SIDs: TaskSIDs{SIDs: []string{"S-1"}}, RequestedBy: "admin"}},
		{TaskKindModeChangeRequest, `{"target_mode":"advanced","requested_by":"admin"}`, &TaskModeChangeRequestPayload{TargetMode: "advanced", RequestedBy: "admin"}},
	}
	for _, tc := range cases {
		got, err := DeviceTask{Kind: tc.kind, Payload: json.RawMessage(tc.payload)}.DecodePayload()
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v want %#v", tc.kind, got, tc.want)
		}
		out, err := json.Marshal(got)
		if err != nil || string(out) != tc.payload {
			t.Errorf("%s: re-marshal %s (%v), want %s", tc.kind, out, err, tc.payload)
		}
	}

	if _, err := DecodeTaskPayload("format_disk", json.RawMessage(`{}`)); !errors.Is(err, ErrUnknownTaskKind) {
		t.Errorf("unknown kind: got %v", err)
	}
	if _, err := DecodeTaskPayload(TaskKindRewrap, json.RawMessage(`{"repo_id":1,"epoch":1,"x":2}`)); err == nil {
		t.Error("unknown payload field accepted")
	}
	if _, err := DecodeTaskPayload(TaskKindProfileConsentRequest, json.RawMessage(`{"request_id":"r","sids":"some","requested_by":"a"}`)); err == nil {
		t.Error(`sids other than "all" accepted`)
	}
}

func TestAISettingsSecretUpdate(t *testing.T) {
	var keep, clear, set AISettingsUpdate
	if err := strictDecode(t, []byte(`{"enabled":true,"base_url":"https://x","model":"m","scopes":[]}`), &keep); err != nil {
		t.Fatal(err)
	}
	if err := strictDecode(t, []byte(`{"enabled":true,"base_url":"https://x","model":"m","scopes":[],"secret":null}`), &clear); err != nil {
		t.Fatal(err)
	}
	if err := strictDecode(t, []byte(`{"enabled":true,"base_url":"https://x","model":"m","scopes":[],"secret":"k"}`), &set); err != nil {
		t.Fatal(err)
	}
	if keep.Secret.Present || !clear.Secret.Present || clear.Secret.Value != nil || set.Secret.Value == nil || *set.Secret.Value != "k" {
		t.Fatalf("secret states: keep=%+v clear=%+v set=%+v", keep.Secret, clear.Secret, set.Secret)
	}
	out, _ := json.Marshal(keep)
	if strings.Contains(string(out), "secret") {
		t.Errorf("omitted secret marshalled: %s", out)
	}
}

func TestDeployEnvRemovalConfirmText(t *testing.T) {
	if got := DeployEnvRemovalConfirmText(3); got != "REMOVE 3 VARIABLES" {
		t.Errorf("got %q", got)
	}
}

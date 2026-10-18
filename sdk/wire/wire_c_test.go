package wire

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestWireCRemovedInRev11 checks that rev 1.0 fields removed by rev 1.1
// (encryption_mode, client_only credentials, managed-metadata listing) are
// rejected by strict decoding.
func TestWireCRemovedInRev11(t *testing.T) {
	cases := []struct {
		name string
		json string
		v    any
	}{
		{"capabilities encryption_modes", `{"encryption_modes":["e2e"]}`, &Capabilities{}},
		{"capabilities server_destination", `{"server_destination":true}`, &Capabilities{}},
		{"repo encryption_mode", `{"id":1,"encryption_mode":"e2e"}`, &Repo{}},
		{"destination credential_scope", `{"id":1,"credential_scope":"client_only"}`, &Destination{}},
		{"destination update credential_scope", `{"credential_scope":"server_held"}`, &DestinationUpdateRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := wireADecodeStrict(tc.json, tc.v); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("want unknown field error, got %v", err)
			}
		})
	}
}

// TestWireCRev10Presence checks fields rev 1.0 shows as always present or
// nullable.
func TestWireCRev10Presence(t *testing.T) {
	checks := []struct {
		v    any
		want []string
	}{
		{Snapshot{}, []string{`"parent_id":null`, `"protected_until":null`, `"status":""`}},
		{SecretState{}, []string{`"secret_updated_at":0`}},
		{DestinationConfig{}, []string{`"path":""`, `"provider":""`, `"path_style":false`}},
		{ApprovalAccepted{ApprovalID: 1}, []string{`"operation_id":null`}},
		{Approval{}, []string{`"decided_by":null`, `"decided_at":null`, `"reason":""`}},
		{DeviceTask{}, []string{`"approval_id":null`, `"claimed_at":0`, `"finished_at":0`}},
		{BackupSettings{}, []string{`"default_retention_policy_id":null`}},
		{RetentionRules{}, []string{`"keep_last":0`, `"keep_within_days":0`}},
		{JobSpec{}, []string{`"ruleset_id":null`, `"use_vss":""`}},
	}
	for _, c := range checks {
		out, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !bytes.Contains(out, []byte(w)) {
				t.Errorf("%T: missing %s in %s", c.v, w, out)
			}
		}
	}
	out, _ := json.Marshal(DeviceTaskCreated{TaskID: 1})
	if bytes.Contains(out, []byte("approval_id")) {
		t.Errorf("ungated task reply has approval_id: %s", out)
	}
}

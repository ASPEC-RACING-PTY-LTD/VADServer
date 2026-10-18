package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
)

// boxDevice is an enrolled device with its X25519 box key.
type boxDevice struct {
	c       *client.Client
	boxPriv vdcrypt.Key
	boxPub  vdcrypt.Key
	signPub ed25519.PublicKey
	id      *wire.ServerIdentity
}

// enrollBox enrols a device that sends box_pub (api-v1 §5A) and approves it.
func (e *env) enrollBox(w *web, login, password, ident string) *boxDevice {
	e.t.Helper()
	base, _ := url.Parse(e.ts.URL)
	c := client.New(base, client.Options{})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	boxPriv, boxPub, _ := vdcrypt.GenerateX25519(nil)
	ctx := context.Background()
	res, err := c.Enroll(ctx, client.EnrollRequest{Login: login, Password: password, DeviceName: ident, OS: "Windows 11", Identifier: ident,
		PublicKey: protocol.EncodePublicKey(pub), ClientVersion: "test", BoxPub: base64.StdEncoding.EncodeToString(boxPub[:]),
		InstallMode: "simple", CoreVersion: "test"})
	if err != nil {
		e.t.Fatalf("enroll: %v", err)
	}
	w.must("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil, 200)
	st, err := c.EnrollStatus(ctx, res.RequestID, res.PollToken)
	if err != nil || st.Status != "approved" {
		e.t.Fatalf("approved: %+v %v", st, err)
	}
	c.DeviceID, c.Key = st.DeviceID, priv
	return &boxDevice{c: c, boxPriv: boxPriv, boxPub: boxPub, signPub: pub, id: st.ServerIdentity}
}

func putKeyFile(t *testing.T, d *boxDevice, repoID int64, kf *vdcrypt.KeyFile) string {
	t.Helper()
	b, err := kf.Encode()
	if err != nil {
		t.Fatal(err)
	}
	oid := vdcrypt.OID(b)
	if _, err := d.c.PutObject(context.Background(), repoID, wire.ObjectKindKeys, oid, bytes.NewReader(b), int64(len(b)), ""); err != nil {
		t.Fatalf("put key file: %v", err)
	}
	return oid
}

func pollOne(t *testing.T, d *boxDevice, kind string) wire.DeviceTask {
	t.Helper()
	tasks, err := d.c.PollTasks(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Kind == kind {
			return task
		}
	}
	t.Fatalf("no %s task in %+v", kind, tasks)
	return wire.DeviceTask{}
}

func keyStatus(t *testing.T, d *boxDevice, repoID int64) *wire.KeyStatus {
	t.Helper()
	ks, err := d.c.KeyStatus(context.Background(), repoID)
	if err != nil {
		t.Fatalf("key status: %v", err)
	}
	return ks
}

func TestEnrolmentKeyExchangeAndKeyShare(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	ctx := context.Background()

	// Without key custody the poll omits server_identity.
	old := e.enrollBox(bob, "bob", "password-bob", "OLD-PC")
	if old.id != nil {
		t.Fatalf("identity without KEK: %+v", old.id)
	}
	// A malformed box_pub is refused.
	base, _ := url.Parse(e.ts.URL)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := client.New(base, client.Options{}).Enroll(ctx, client.EnrollRequest{Login: "alice", Password: "password-alice",
		DeviceName: "X", Identifier: "x", PublicKey: protocol.EncodePublicKey(pub), BoxPub: "AAAA"}); err == nil {
		t.Fatal("bad box_pub accepted")
	} else if st, code := apiCode(t, err); st != 400 || code != "bad_box_pub" {
		t.Fatalf("bad box_pub: %d %s", st, code)
	}

	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(ctx, e.st, kek)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetKeys(m)
	ident, _ := m.Identity()

	a := e.enrollBox(alice, "alice", "password-alice", "PC-A")
	if a.id == nil || a.id.SIKFingerprint != ident.SIKFingerprint || a.id.SEKVersion != 1 {
		t.Fatalf("server identity: %+v", a.id)
	}
	if dk, err := e.st.DeviceKey(ctx, a.c.DeviceID); err != nil || dk.SIKPinnedFingerprint != ident.SIKFingerprint ||
		dk.BoxPub != base64.StdEncoding.EncodeToString(a.boxPub[:]) {
		t.Fatalf("device keys: %+v %v", dk, err)
	}

	// Device A initialises a repository: device, escrow and recovery wraps.
	rp, err := a.c.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC backups"})
	if err != nil {
		t.Fatal(err)
	}
	srow, _ := e.st.Repository(ctx, rp.ID)
	aliceID := srow.UserID
	repoID, _ := vdcrypt.ParseRepoID(aliceRepoUUID)
	rmk, _ := vdcrypt.RandomKey(nil)
	dk, _ := vdcrypt.RandomKey(nil)
	full := vdcrypt.FullBundle(vdcrypt.EpochSecret{Epoch: 1, RMK: rmk}, dk)
	sekPub, server, _ := m.CurrentSEK()
	mk := func(p vdcrypt.KeyFileParams) *vdcrypt.KeyFile {
		p.Repo = repoID
		kf, err := vdcrypt.NewKeyFile(nil, p, full)
		if err != nil {
			t.Fatal(err)
		}
		return kf
	}
	_, recPub, _ := vdcrypt.GenerateX25519(nil)
	putKeyFile(t, a, rp.ID, mk(vdcrypt.KeyFileParams{Kind: vdcrypt.KindDevice, Label: "PC-A", RecipientPub: a.boxPub,
		Device: &vdcrypt.KeyFileDevice{ID: a.c.DeviceID, SignPub: a.signPub}}))
	putKeyFile(t, a, rp.ID, mk(vdcrypt.KeyFileParams{Kind: vdcrypt.KindEscrow, Label: "escrow", RecipientPub: sekPub, Server: &server}))
	putKeyFile(t, a, rp.ID, mk(vdcrypt.KeyFileParams{Kind: vdcrypt.KindRecovery, Label: "recovery", RecipientPub: recPub}))

	ks := keyStatus(t, a, rp.ID)
	if ks.CurrentEpoch != 1 || len(ks.Recipients) != 3 || !ks.RecoveryWrappedCurrent || ks.EscrowMode != "full" {
		t.Fatalf("status: %+v", ks)
	}
	if out := alice.must("GET", fmt.Sprintf("/api/v1/repos/%d/keys/status", rp.ID), nil, 200); out["current_epoch"] != float64(1) {
		t.Fatalf("session status: %v", out)
	}

	// Full escrow: approving device B makes the server write B's wrap and
	// send key_share {repo_id, key_file_oid}.
	b := e.enrollBox(alice, "alice", "password-alice", "PC-B")
	task := pollOne(t, b, wire.TaskKindKeyShare)
	var share wire.TaskKeySharePayload
	if err := json.Unmarshal(task.Payload, &share); err != nil || share.RepoID != rp.ID || share.KeyFileOID == "" || share.TargetDeviceID != 0 {
		t.Fatalf("key_share payload: %s %v", task.Payload, err)
	}
	rc, err := b.c.GetObject(ctx, rp.ID, wire.ObjectKindKeys, share.KeyFileOID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	kf, err := vdcrypt.DecodeKeyFile(raw)
	if err != nil || kf.Kind != vdcrypt.KindDevice || kf.Device.ID != b.c.DeviceID {
		t.Fatalf("device key file: %+v %v", kf, err)
	}
	if got, err := kf.Open(b.boxPriv); err != nil || !got.RMK.Equal(rmk) || !got.DK.Equal(dk) {
		t.Fatalf("open shared wrap: %v", err)
	}
	if rows, _ := e.st.ListKeyAudit(ctx, rp.ID, 0, 0); len(rows) != 1 || rows[0].Purpose != keys.PurposeRewrap {
		t.Fatalf("audit: %+v", rows)
	}
	res, _ := json.Marshal(client.KeyTaskResult{RepoID: rp.ID, Epoch: 1, KeyFileOID: share.KeyFileOID})
	if err := b.c.TaskResult(ctx, task.ID, wire.TaskStatusDone, res); err != nil {
		t.Fatal(err)
	}
	if ks := keyStatus(t, b, rp.ID); len(ks.Recipients) != 4 {
		t.Fatalf("status after share: %+v", ks)
	}

	// SEK rotation re-wraps the current epoch's escrow wrap.
	out := admin.must("POST", "/api/v1/admin/keys/rotate-server", nil, 202)
	waitOperation(t, admin, int64(out["operation_id"].(float64)), wire.OperationStateSucceeded)
	ks = keyStatus(t, a, rp.ID)
	escrows := 0
	for _, r := range ks.Recipients {
		if r.Kind == wire.KeyRecipientEscrow && r.Epoch == 1 {
			escrows++
		}
	}
	if escrows != 2 {
		t.Fatalf("escrow wraps after SEK rotation: %+v", ks)
	}

	// Escrow change: owner-only PATCH -> high-risk approval -> rewrap task.
	if code, out := alice.req("PATCH", fmt.Sprintf("/api/v1/repos/%d", rp.ID), map[string]any{"escrow_mode": "bogus"}); code != 422 {
		t.Fatalf("bad mode: %d %v", code, out)
	}
	if code, out := alice.req("PATCH", fmt.Sprintf("/api/v1/repos/%d", rp.ID), map[string]any{"escrow_mode": "full"}); code != 409 {
		t.Fatalf("same mode: %d %v", code, out)
	}
	acc := alice.must("PATCH", fmt.Sprintf("/api/v1/repos/%d", rp.ID), map[string]any{"escrow_mode": "metadata"}, 202)
	apprID := int64(acc["approval_id"].(float64))
	alice.must("POST", fmt.Sprintf("/api/v1/approvals/%d/approve", apprID), map[string]any{"confirm_text": "PC backups"}, 200)
	if r, _ := e.st.Repository(ctx, rp.ID); r.EscrowMode != "metadata" {
		t.Fatalf("escrow mode: %+v", r)
	}
	open, _ := e.st.OpenTasks(ctx, aliceID, wire.TaskKindRewrap)
	if len(open) != 1 || (open[0].DeviceID != a.c.DeviceID && open[0].DeviceID != b.c.DeviceID) {
		t.Fatalf("rewrap tasks: %+v", open)
	}
	var rw wire.TaskRewrapPayload
	if err := json.Unmarshal([]byte(open[0].Payload), &rw); err != nil || rw.RepoID != rp.ID || rw.Epoch != 2 {
		t.Fatalf("rewrap payload: %s", open[0].Payload)
	}

	// Manual rotation reuses the open rewrap.
	out = alice.must("POST", fmt.Sprintf("/api/v1/repos/%d/keys/rotate", rp.ID), nil, 202)
	op := waitOperation(t, alice, int64(out["operation_id"].(float64)), wire.OperationStateSucceeded)
	if r := op["result"].(map[string]any); r["task_id"] != float64(open[0].ID) || r["epoch"] != float64(2) {
		t.Fatalf("rotate result: %v", op)
	}

	// Metadata escrow: device C is shared by an existing device.
	c := e.enrollBox(alice, "alice", "password-alice", "PC-C")
	shares, _ := e.st.OpenTasks(ctx, aliceID, wire.TaskKindKeyShare)
	if len(shares) != 1 || shares[0].DeviceID == c.c.DeviceID {
		t.Fatalf("metadata key_share: %+v", shares)
	}
	var ms wire.TaskKeySharePayload
	_ = json.Unmarshal([]byte(shares[0].Payload), &ms)
	if ms.RepoID != rp.ID || ms.TargetDeviceID != c.c.DeviceID || ms.KeyFileOID != "" ||
		ms.TargetBoxPub != base64.StdEncoding.EncodeToString(c.boxPub[:]) || ms.TargetSignPub != protocol.EncodePublicKey(c.signPub) {
		t.Fatalf("metadata payload: %+v", ms)
	}
	// keys/status does not queue a duplicate.
	keyStatus(t, c, rp.ID)
	if again, _ := e.st.OpenTasks(ctx, aliceID, wire.TaskKindKeyShare); len(again) != 1 {
		t.Fatalf("duplicate key_share: %+v", again)
	}

	// A device whose pinned SIK differs gets 409 sik_mismatch.
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE device_keys SET sik_pinned_fingerprint = 'AAAA-BBBB' WHERE device_id = ?`, c.c.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.c.KeyStatus(ctx, rp.ID); err == nil {
		t.Fatal("pin mismatch accepted")
	} else if st, code := apiCode(t, err); st != 409 || code != wire.CodeSIKMismatch {
		t.Fatalf("sik mismatch: %d %s", st, code)
	}

	// Another user's repository is not found.
	if code, _ := bob.req("GET", fmt.Sprintf("/api/v1/repos/%d/keys/status", rp.ID), nil); code != 404 {
		t.Fatalf("foreign status: %d", code)
	}
	if code, _ := bob.req("POST", fmt.Sprintf("/api/v1/repos/%d/keys/rotate", rp.ID), nil); code != 404 {
		t.Fatalf("foreign rotate: %d", code)
	}
}

func TestRevocationQueuesRotation(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	ctx := context.Background()
	a := e.enrollBox(alice, "alice", "password-alice", "PC-A")
	b := e.enrollBox(alice, "alice", "password-alice", "PC-B")
	rp, err := a.c.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC backups"})
	if err != nil {
		t.Fatal(err)
	}
	repoID, _ := vdcrypt.ParseRepoID(aliceRepoUUID)
	rmk, _ := vdcrypt.RandomKey(nil)
	dk, _ := vdcrypt.RandomKey(nil)
	full := vdcrypt.FullBundle(vdcrypt.EpochSecret{Epoch: 1, RMK: rmk}, dk)
	for _, d := range []*boxDevice{a, b} {
		kf, err := vdcrypt.NewKeyFile(nil, vdcrypt.KeyFileParams{Repo: repoID, Kind: vdcrypt.KindDevice, RecipientPub: d.boxPub,
			Device: &vdcrypt.KeyFileDevice{ID: d.c.DeviceID, SignPub: d.signPub}}, full)
		if err != nil {
			t.Fatal(err)
		}
		putKeyFile(t, a, rp.ID, kf)
	}
	// No escrow wrap, no KEK: rotation still goes to a device.
	alice.must("POST", fmt.Sprintf("/api/v1/devices/%d/revoke", b.c.DeviceID), nil, 200)
	task := pollOne(t, a, wire.TaskKindRewrap)
	var rw wire.TaskRewrapPayload
	if err := json.Unmarshal(task.Payload, &rw); err != nil || rw.RepoID != rp.ID || rw.Epoch != 2 {
		t.Fatalf("rewrap: %s", task.Payload)
	}
	// With no remaining key holder, manual rotation is refused.
	alice.must("POST", fmt.Sprintf("/api/v1/devices/%d/revoke", a.c.DeviceID), nil, 200)
	if out := alice.must("POST", fmt.Sprintf("/api/v1/repos/%d/keys/rotate", rp.ID), nil, 409); errCode(out) != "no_key_holder" {
		t.Fatalf("no holder: %v", out)
	}
}

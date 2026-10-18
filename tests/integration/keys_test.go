package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/testserver"
)

const (
	keysFullUUID = "3c4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"
	keysMetaUUID = "4c4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"
)

func keyStatusOf(t *testing.T, d *keyDevice, repoID int64) *wire.KeyStatus {
	t.Helper()
	ks, err := d.c.KeyStatus(context.Background(), repoID)
	if err != nil {
		t.Fatalf("%s: key status: %v", d.name, err)
	}
	return ks
}

func hasDeviceWrap(ks *wire.KeyStatus, deviceID int64) bool {
	for _, r := range ks.Recipients {
		if r.Kind == wire.KeyRecipientDevice && r.DeviceID == deviceID && r.Epoch == ks.CurrentEpoch {
			return true
		}
	}
	return false
}

// TestKeysEnrolPinAndKeyShare: enrolment with box_pub returns the server
// identity (SIK pin) → first-device repository init over the server backend
// (device, escrow and recovery wraps) → a second device receives the keys
// by the server (full escrow). Device-to-device sharing (metadata escrow)
// is TestKeysMetadataEscrowShare.
func TestKeysEnrolPinAndKeyShare(t *testing.T) {
	srv := testserver.Start(t)
	ctx := context.Background()

	// Enrolment (SIK pin).
	a := enrolKeyDevice(t, srv, "PC-A")
	ci := checkIdentity(t, a.ident)
	if a.ident.SEKVersion != 1 {
		t.Fatalf("sek_version %d", a.ident.SEKVersion)
	}
	if dk, err := srv.Store.DeviceKey(ctx, a.c.DeviceID); err != nil || dk.SIKPinnedFingerprint != ci.fp ||
		dk.BoxPub != base64.StdEncoding.EncodeToString(a.boxPub[:]) {
		t.Fatalf("stored device keys: %+v %v", dk, err)
	}
	// GET /keys/server matches the pin; a different pin is refused.
	if id, err := a.c.VerifyServerIdentity(ctx, ci.fp); err != nil || id.SIKPub != a.ident.SIKPub || id.SEKPub != a.ident.SEKPub {
		t.Fatalf("verify identity: %+v %v", id, err)
	}
	if _, err := a.c.VerifyServerIdentity(ctx, "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-0000-1111"); !client.IsCode(err, wire.CodeSIKMismatch) {
		t.Fatalf("wrong pin: %v", err)
	}

	// First-device keys: full escrow.
	fullID, fullRepo, recovery := initRepo(t, a, ci, keysFullUUID, "Full", repo.EscrowFull)
	if recovery == "" {
		t.Fatal("no recovery key")
	}
	ks := keyStatusOf(t, a, fullID)
	if ks.CurrentEpoch != 1 || len(ks.Recipients) != 3 || !ks.RecoveryWrappedCurrent || ks.EscrowMode != repo.EscrowFull || !hasDeviceWrap(ks, a.c.DeviceID) {
		t.Fatalf("full key status: %+v", ks)
	}
	// The recovery key opens the repository on its own.
	if cred, err := repo.RecoveryCredential(recovery); err != nil {
		t.Fatal(err)
	} else if r, err := repo.Open(ctx, a.backend(fullID), cred, repo.Options{}); err != nil {
		t.Fatalf("open with recovery key: %v", err)
	} else {
		r.Close()
	}

	// Second device, full escrow: the server writes B's wrap and sends
	// key_share {repo_id, key_file_oid}.
	b := enrolKeyDevice(t, srv, "PC-B")
	if checkIdentity(t, b.ident).fp != ci.fp {
		t.Fatal("second device got a different server identity")
	}
	task := b.keyTask(t, wire.TaskKindKeyShare, func(task wire.DeviceTask) bool {
		var p wire.TaskKeySharePayload
		return json.Unmarshal(task.Payload, &p) == nil && p.RepoID == fullID
	})
	var share wire.TaskKeySharePayload
	if err := json.Unmarshal(task.Payload, &share); err != nil || share.KeyFileOID == "" || share.TargetDeviceID != 0 {
		t.Fatalf("full key_share payload: %s %v", task.Payload, err)
	}
	bRepo := b.openRepo(t, fullID)
	if bRepo.ID() != fullRepo.ID() || bRepo.Epoch() != 1 {
		t.Fatalf("B opened %s epoch %d", bRepo.ID(), bRepo.Epoch())
	}
	ak, _ := fullRepo.Keys()
	bk, _ := bRepo.Keys()
	if !ak.ID.Equal(bk.ID) || !ak.FileHash.Equal(bk.FileHash) {
		t.Fatal("B derived different repository keys")
	}
	if err := b.c.FinishKeyShare(ctx, task.ID, client.KeyTaskResult{RepoID: fullID, Epoch: 1, KeyFileOID: share.KeyFileOID}); err != nil {
		t.Fatal(err)
	}
	if ks := keyStatusOf(t, b, fullID); len(ks.Recipients) != 4 || !hasDeviceWrap(ks, b.c.DeviceID) {
		t.Fatalf("status after full share: %+v", ks)
	}
}

// TestKeysMetadataEscrowShare: under metadata escrow the server cannot
// wrap keys itself, so a newly approved device is shared by a key holder
// (key_share {target_device_id, target_box_pub, target_sign_pub}).
func TestKeysMetadataEscrowShare(t *testing.T) {
	srv := testserver.Start(t)
	ctx := context.Background()
	a := enrolKeyDevice(t, srv, "PC-A")
	ci := checkIdentity(t, a.ident)

	// First-device keys: metadata escrow.
	metaID, metaRepo, _ := initRepo(t, a, ci, keysMetaUUID, "Meta", repo.EscrowMetadata)
	if ks := keyStatusOf(t, a, metaID); ks.EscrowMode != repo.EscrowMetadata || len(ks.Recipients) != 3 {
		t.Fatalf("meta key status: %+v", ks)
	}

	// A second device: device A (the key holder) is asked to
	// wrap the current epoch to C.
	c := enrolKeyDevice(t, srv, "PC-C")
	mt := a.keyTask(t, wire.TaskKindKeyShare, func(task wire.DeviceTask) bool {
		var p wire.TaskKeySharePayload
		return json.Unmarshal(task.Payload, &p) == nil && p.RepoID == metaID
	})
	var ms wire.TaskKeySharePayload
	if err := json.Unmarshal(mt.Payload, &ms); err != nil || ms.KeyFileOID != "" || ms.TargetDeviceID != c.c.DeviceID ||
		ms.TargetBoxPub != base64.StdEncoding.EncodeToString(c.boxPub[:]) || ms.TargetSignPub != protocol.EncodePublicKey(c.signPub) {
		t.Fatalf("metadata key_share payload: %s %v", mt.Payload, err)
	}
	boxRaw, _ := base64.StdEncoding.DecodeString(ms.TargetBoxPub)
	targetBox, err := vdcrypt.KeyFromBytes(boxRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := metaRepo.AddRecipient(ctx, repo.DeviceRecipient(targetBox, ms.TargetDeviceID, c.signPub, "PC-C")); err != nil {
		t.Fatalf("add recipient: %v", err)
	}
	var newOID string
	for oid, kf := range metaRepo.KeyFiles() {
		if kf.Kind == vdcrypt.KindDevice && kf.Device != nil && kf.Device.ID == c.c.DeviceID {
			newOID = oid
		}
	}
	if newOID == "" {
		t.Fatal("no key file for C after AddRecipient")
	}
	if err := a.c.FinishKeyShare(ctx, mt.ID, client.KeyTaskResult{RepoID: metaID, Epoch: 1, KeyFileOID: newOID}); err != nil {
		t.Fatal(err)
	}
	if ks := keyStatusOf(t, c, metaID); !hasDeviceWrap(ks, c.c.DeviceID) || len(ks.Recipients) != 4 {
		t.Fatalf("status after metadata share: %+v", ks)
	}
	cRepo := c.openRepo(t, metaID)
	mk, _ := metaRepo.Keys()
	ck, _ := cRepo.Keys()
	if !mk.ID.Equal(ck.ID) || cRepo.MetadataOnly() {
		t.Fatalf("C repo keys: metadata only %v", cRepo.MetadataOnly())
	}
}

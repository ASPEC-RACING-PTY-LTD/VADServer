package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

const (
	aliceRepoUUID = "0b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"
	bobRepoUUID   = "1b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"
)

// signedPut sends a raw signed PUT whose declared body hash may differ from
// the body, which the SDK client refuses to do.
func signedPut(t *testing.T, e *env, dc *client.Client, path string, body []byte, declared string) (int, string) {
	t.Helper()
	h, err := protocol.Sign(dc.Key, strconv.FormatInt(dc.DeviceID, 10), "PUT", path, declared, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("PUT", e.ts.URL+path, bytes.NewReader(body))
	req.Header.Set(protocol.HeaderDevice, h.DeviceID)
	req.Header.Set(protocol.HeaderTimestamp, h.Timestamp)
	req.Header.Set(protocol.HeaderNonce, h.Nonce)
	req.Header.Set(protocol.HeaderBodySHA256, h.BodySHA256)
	req.Header.Set(protocol.HeaderSignature, h.Signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, errCode(m)
}

func readObject(t *testing.T, dc *client.Client, repoID int64, kind, oid string, off, length int64) []byte {
	t.Helper()
	rc, err := dc.GetObject(context.Background(), repoID, kind, oid, off, length)
	if err != nil {
		t.Fatalf("get %s/%s: %v", kind, oid, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBackupReposAndObjects(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	bc := e.enrollDevice(bob, "bob", "password-bob")
	ctx := context.Background()

	var caps wire.Capabilities
	if err := dc.Call(ctx, "GET", "/api/v1/backup/capabilities", nil, &caps); err != nil || caps.MaxObjectBytes != 64<<20 || caps.MaxMissingBatch != 20000 || caps.DefaultEscrowMode != "full" {
		t.Fatalf("capabilities: %+v %v", caps, err)
	}

	// Create on the built-in server destination; replay is idempotent.
	repo, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC backups"})
	if err != nil || repo.ID == 0 || repo.DestinationID == 0 || repo.EscrowMode != "full" || repo.CreatedByDeviceID != dc.DeviceID || repo.CurrentEpoch != 1 {
		t.Fatalf("create: %+v %v", repo, err)
	}
	if again, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC backups", DestinationID: repo.DestinationID}); err != nil || again.ID != repo.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if st, code := apiCode(t, dc.Call(ctx, "POST", "/api/v1/repos", wire.RepoCreateRequest{RepoUUID: bobRepoUUID, Name: "x", FormatVersion: 1}, nil)); st != 409 || code != "repo_exists" {
		t.Fatalf("second repo on destination: %d %s", st, code)
	}
	if st, code := apiCode(t, dc.Call(ctx, "POST", "/api/v1/repos", wire.RepoCreateRequest{RepoUUID: "NOT-A-UUID", Name: "x", FormatVersion: 1}, nil)); st != 422 || code != "validation" {
		t.Fatalf("bad uuid: %d %s", st, code)
	}
	if code, _ := alice.req("POST", "/api/v1/repos", map[string]any{"repo_uuid": bobRepoUUID, "name": "x", "format_version": 1}); code != 401 && code != 403 {
		t.Fatalf("session create must be refused (dev only): %d", code)
	}
	bobRepo, err := bc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: bobRepoUUID, Name: "Bob", DestinationID: repo.DestinationID})
	if err != nil || bobRepo.DestinationID != repo.DestinationID {
		t.Fatalf("bob create: %+v %v", bobRepo, err)
	}

	// Listing and isolation.
	if l := alice.must("GET", "/api/v1/repos", nil, 200); len(l["items"].([]any)) != 1 {
		t.Fatalf("session list: %v", l)
	}
	if out := alice.must("GET", fmt.Sprintf("/api/v1/repos/%d", bobRepo.ID), nil, 404); errCode(out) != wire.CodeRepoNotFound {
		t.Fatalf("foreign repo: %v", out)
	}

	// Objects: idempotent PUT, HEAD, GET with Range, list, missing.
	pack := make([]byte, 3<<20) // larger than the 2 MiB signed-body limit
	rand.Read(pack)
	oid := protocol.SHA256Hex(pack)
	created, err := dc.PutObject(ctx, repo.ID, "data", oid, bytes.NewReader(pack), int64(len(pack)), "")
	if err != nil || !created {
		t.Fatalf("put: %v %v", created, err)
	}
	if created, err = dc.PutObject(ctx, repo.ID, "data", oid, bytes.NewReader(pack), int64(len(pack)), ""); err != nil || created {
		t.Fatalf("replay put: %v %v", created, err)
	}
	onDisk := filepath.Join(e.storage, "default", "backup", "repo", aliceRepoUUID, "data", oid[:2], oid)
	if fi, err := os.Stat(onDisk); err != nil || fi.Size() != int64(len(pack)) {
		t.Fatalf("on-disk object: %v", err)
	}
	if ok, size, err := dc.HeadObject(ctx, repo.ID, "data", oid); err != nil || !ok || size != int64(len(pack)) {
		t.Fatalf("head: %v %d %v", ok, size, err)
	}
	if !bytes.Equal(readObject(t, dc, repo.ID, "data", oid, 0, 0), pack) {
		t.Fatal("full get differs")
	}
	if got := readObject(t, dc, repo.ID, "data", oid, 1000, 10); !bytes.Equal(got, pack[1000:1010]) {
		t.Fatal("range get differs")
	}
	if got := readObject(t, dc, repo.ID, "data", oid, int64(len(pack))-5, 0); !bytes.Equal(got, pack[len(pack)-5:]) {
		t.Fatal("open range get differs")
	}
	resp, err := alice.c.Get(fmt.Sprintf("%s/api/v1/repos/%d/objects/data/%s", e.ts.URL, repo.ID, oid))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != int64(len(pack)) {
		t.Fatalf("session get: %d %d", resp.StatusCode, resp.ContentLength)
	}
	small := []byte("index blob")
	smallOID := protocol.SHA256Hex(small)
	if _, err := dc.PutObject(ctx, repo.ID, "index", smallOID, bytes.NewReader(small), int64(len(small)), ""); err != nil {
		t.Fatal(err)
	}
	if l, err := dc.ListObjects(ctx, repo.ID, "data", "", 0); err != nil || len(l.Items) != 1 || l.Items[0].OID != oid || l.Items[0].Size != int64(len(pack)) {
		t.Fatalf("list data: %+v %v", l, err)
	}
	if l, err := dc.ListObjects(ctx, repo.ID, "", "", 1); err != nil || len(l.Items) != 1 || l.NextCursor == "" {
		t.Fatalf("list page: %+v %v", l, err)
	}
	absent := protocol.SHA256Hex([]byte("absent"))
	if m, err := dc.MissingObjects(ctx, repo.ID, "data", []string{oid, absent}); err != nil || len(m) != 1 || m[0] != absent {
		t.Fatalf("missing: %v %v", m, err)
	}
	if ok, _, err := dc.HeadObject(ctx, repo.ID, "data", absent); err != nil || ok {
		t.Fatalf("head absent: %v %v", ok, err)
	}
	if r, err := dc.Repo(ctx, repo.ID); err != nil || r.Usage.Objects != 2 || r.Usage.StoredBytes != int64(len(pack)+len(small)) {
		t.Fatalf("usage: %+v %v", r, err)
	}

	// Hash failures: body ≠ oid is 422; body ≠ signed hash is 400.
	other := []byte("not the oid")
	if _, err := dc.PutObject(ctx, repo.ID, "keys", absent, bytes.NewReader(other), int64(len(other)), protocol.SHA256Hex(other)); err == nil {
		t.Fatal("hash mismatch accepted")
	} else if st, code := apiCode(t, err); st != 422 || code != wire.CodeObjectHashMismatch {
		t.Fatalf("hash mismatch: %d %s", st, code)
	}
	objPath := fmt.Sprintf("/api/v1/repos/%d/objects/keys/%s", repo.ID, absent)
	if st, code := signedPut(t, e, dc, objPath, other, absent); st != 400 || code != "body_mismatch" {
		t.Fatalf("transport mismatch: %d %s", st, code)
	}
	if m, _ := dc.MissingObjects(ctx, repo.ID, "keys", []string{absent}); len(m) != 1 {
		t.Fatal("failed PUT recorded an object")
	}
	if st, code := apiCode(t, func() error {
		_, err := dc.PutObject(ctx, repo.ID, "packs", absent, bytes.NewReader(nil), 0, protocol.EmptySHA256)
		return err
	}()); st != 400 || code != wire.CodeInvalidOID {
		t.Fatalf("bad kind: %d %s", st, code)
	}

	// Cross-user access is repo_not_found for devices too.
	if _, err := bc.PutObject(ctx, repo.ID, "index", smallOID, bytes.NewReader(small), int64(len(small)), ""); err == nil {
		t.Fatal("foreign put accepted")
	} else if st, code := apiCode(t, err); st != 404 || code != wire.CodeRepoNotFound {
		t.Fatalf("foreign put: %d %s", st, code)
	}
	if _, err := bc.GetObject(ctx, repo.ID, "data", oid, 0, 0); err == nil {
		t.Fatal("foreign get accepted")
	}

	// Quota.
	if _, err := e.st.DB.Exec("UPDATE users SET backup_quota_bytes = ? WHERE username = 'bob'", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.PutObject(ctx, bobRepo.ID, "index", smallOID, bytes.NewReader(small), int64(len(small)), ""); err == nil {
		t.Fatal("over-quota put accepted")
	} else if st, code := apiCode(t, err); st != 413 || code != wire.CodeBackupQuotaExceeded {
		t.Fatalf("quota: %d %s", st, code)
	}
}

func TestBackupConfigNeedsExclusiveLock(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	repo, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC"})
	if err != nil {
		t.Fatal(err)
	}
	putConfig := func(b []byte) (bool, error) {
		return dc.PutObject(ctx, repo.ID, "config", wire.ObjectConfigOID, bytes.NewReader(b), int64(len(b)), protocol.SHA256Hex(b))
	}
	v1 := []byte(`{"format":"vaultdock.repo","v":1,"escrow_mode":"full"}`)

	// No lock, then a lock held by the server: 409 repo_locked.
	_, err = putConfig(v1)
	if d, ok := client.RepoLocked(err); !ok || d.Holder != "" {
		t.Fatalf("no lock: %v %+v", err, d)
	}
	exp := time.Now().Add(time.Minute).UnixMilli()
	lock := func(id, kind string, dev any, holder string) {
		t.Helper()
		hk := "server"
		if dev != nil {
			hk = "device"
		}
		if _, err := e.st.DB.Exec(`INSERT INTO repo_locks(id, repo_id, kind, holder_kind, holder_device_id, holder, acquired_at, refreshed_at, expires_at)
			VALUES(?,?,?,?,?,?,1,1,?)`, id, repo.ID, kind, hk, dev, holder, exp); err != nil {
			t.Fatal(err)
		}
	}
	lock("srv", "shared", nil, "verify")
	_, err = putConfig(v1)
	if d, ok := client.RepoLocked(err); !ok || d.Holder != "verify" || d.ExpiresAt != exp {
		t.Fatalf("shared server lock: %v %+v", err, d)
	}

	// With an exclusive lock: create 201, identical 200, replace 201.
	lock("mine", "exclusive", dc.DeviceID, "{}")
	if created, err := putConfig(v1); err != nil || !created {
		t.Fatalf("create config: %v %v", created, err)
	}
	if created, err := putConfig(v1); err != nil || created {
		t.Fatalf("identical config: %v %v", created, err)
	}
	v2 := []byte(`{"format":"vaultdock.repo","v":1,"escrow_mode":"metadata"}`)
	if created, err := putConfig(v2); err != nil || !created {
		t.Fatalf("replace config: %v %v", created, err)
	}
	if got := readObject(t, dc, repo.ID, "config", wire.ObjectConfigOID, 0, 0); !bytes.Equal(got, v2) {
		t.Fatalf("config = %s", got)
	}
	if ok, size, err := dc.HeadObject(ctx, repo.ID, "config", wire.ObjectConfigOID); err != nil || !ok || size != int64(len(v2)) {
		t.Fatalf("head config: %v %d %v", ok, size, err)
	}
	// A config body that differs from its signed hash is a transport error,
	// and the stored config is untouched.
	path := fmt.Sprintf("/api/v1/repos/%d/objects/config/%s", repo.ID, wire.ObjectConfigOID)
	if st, code := signedPut(t, e, dc, path, []byte(`{"x":1}`), protocol.SHA256Hex([]byte(`{"y":2}`))); st != 400 || code != "body_mismatch" {
		t.Fatalf("config transport mismatch: %d %s", st, code)
	}
	if got := readObject(t, dc, repo.ID, "config", wire.ObjectConfigOID, 0, 0); !bytes.Equal(got, v2) {
		t.Fatalf("config after failed replace = %s", got)
	}
	// A non-zero oid for config is invalid.
	if st, code := signedPut(t, e, dc, fmt.Sprintf("/api/v1/repos/%d/objects/config/%s", repo.ID, protocol.SHA256Hex(v1)), v1, protocol.SHA256Hex(v1)); st != 400 || code != wire.CodeInvalidOID {
		t.Fatalf("config oid: %d %s", st, code)
	}
}

// Key file PUTs are checked against the repository (INT-FIX-E): a key file
// of another repository or a body that is not a key file is 422 validation
// naming the field, and key files are capped at 64 KiB.
func TestBackupKeyFilePutChecksRepo(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	repo, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC"})
	if err != nil {
		t.Fatal(err)
	}
	rmk, _ := vdcrypt.RandomKey(nil)
	dk, _ := vdcrypt.RandomKey(nil)
	full := vdcrypt.FullBundle(vdcrypt.EpochSecret{Epoch: 1, RMK: rmk}, dk)
	_, recPub, _ := vdcrypt.GenerateX25519(nil)
	keyFile := func(uuid string) []byte {
		t.Helper()
		id, err := vdcrypt.ParseRepoID(uuid)
		if err != nil {
			t.Fatal(err)
		}
		kf, err := vdcrypt.NewKeyFile(nil, vdcrypt.KeyFileParams{Repo: id, Kind: vdcrypt.KindRecovery, Label: "recovery", RecipientPub: recPub}, full)
		if err != nil {
			t.Fatal(err)
		}
		b, err := kf.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	put := func(b []byte) (bool, error) {
		return dc.PutObject(ctx, repo.ID, wire.ObjectKindKeys, vdcrypt.OID(b), bytes.NewReader(b), int64(len(b)), "")
	}
	field := func(err error) string {
		t.Helper()
		var ae *client.APIError
		if !errors.As(err, &ae) || ae.Status != 422 || ae.Code != "validation" {
			t.Fatalf("want 422 validation, got %v", err)
		}
		var d struct {
			Fields map[string]string `json:"fields"`
		}
		_ = json.Unmarshal(ae.Details, &d)
		for k := range d.Fields {
			return k
		}
		return ""
	}

	foreign := keyFile(bobRepoUUID)
	if _, err := put(foreign); field(err) != "repo" {
		t.Fatalf("foreign key file: %v", err)
	}
	if m, _ := dc.MissingObjects(ctx, repo.ID, wire.ObjectKindKeys, []string{vdcrypt.OID(foreign)}); len(m) != 1 {
		t.Fatal("rejected key file recorded")
	}
	if _, err := put([]byte(`{"v":1,"repo":"nope"}`)); field(err) != "body" {
		t.Fatalf("garbage key file: %v", err)
	}
	big := bytes.Repeat([]byte(" "), 64<<10+1)
	if st, code := apiCode(t, func() error { _, err := put(big); return err }()); st != 413 || code != "too_large" {
		t.Fatalf("oversized key file: %d %s", st, code)
	}
	own := keyFile(aliceRepoUUID)
	if created, err := put(own); err != nil || !created {
		t.Fatalf("own key file: %v %v", created, err)
	}
	if created, err := put(own); err != nil || created {
		t.Fatalf("own key file replay: %v %v", created, err)
	}
	if got := readObject(t, dc, repo.ID, wire.ObjectKindKeys, vdcrypt.OID(own), 0, 0); !bytes.Equal(got, own) {
		t.Fatal("stored key file differs")
	}
}

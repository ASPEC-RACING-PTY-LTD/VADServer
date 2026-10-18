package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// recorded is one request the fake server accepted.
type recorded struct {
	Method, URI string
	Header      http.Header
	Length      int64
	Body        []byte
}

// fakeServer verifies the VD1 signature and the body hash of every request
// like the real server, then dispatches on "METHOD /path".
type fakeServer struct {
	t      *testing.T
	pub    ed25519.PublicKey
	mu     sync.Mutex
	reqs   []recorded
	routes map[string]func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newFake(t *testing.T) (*fakeServer, *Client) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, pub: pub, routes: map[string]func(http.ResponseWriter, *http.Request, []byte){}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	c := New(base, Options{Timeout: 10 * time.Second})
	c.DeviceID, c.Key = 7, priv
	return f, c
}

func (f *fakeServer) handle(route string, h func(w http.ResponseWriter, r *http.Request, body []byte)) {
	f.routes[route] = h
}

func (f *fakeServer) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		f.t.Fatal("no request recorded")
	}
	return f.reqs[len(f.reqs)-1]
}

func writeErr(w http.ResponseWriter, status int, code string, details any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code + " message", "details": details}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := protocol.SignedHeaders{
		DeviceID:   r.Header.Get(protocol.HeaderDevice),
		Timestamp:  r.Header.Get(protocol.HeaderTimestamp),
		Nonce:      r.Header.Get(protocol.HeaderNonce),
		BodySHA256: r.Header.Get(protocol.HeaderBodySHA256),
		Signature:  r.Header.Get(protocol.HeaderSignature),
	}
	if !strings.HasSuffix(r.URL.Path, "/info") {
		if _, err := protocol.Verify(f.pub, r.Method, r.URL.RequestURI(), h, time.Now()); err != nil || h.DeviceID != "7" {
			writeErr(w, 401, "bad_signature", nil)
			return
		}
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return // aborted upload
	}
	if r.Header.Get(protocol.HeaderSignature) != "" && protocol.SHA256Hex(body) != h.BodySHA256 {
		writeErr(w, 422, wire.CodeObjectHashMismatch, nil)
		return
	}
	if r.ContentLength >= 0 && int64(len(body)) != r.ContentLength {
		writeErr(w, 400, "length_mismatch", nil)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Length: r.ContentLength, Body: body})
	f.mu.Unlock()
	route, ok := f.routes[r.Method+" "+r.URL.Path]
	if !ok {
		writeErr(w, 404, "not_found", nil)
		return
	}
	route(w, r, body)
}

func TestPutObjectStreamed(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("vaultdock-pack "), 50000) // ~750 KB
	oid := protocol.SHA256Hex(data)
	path := "/api/v1/repos/3/objects/data/" + oid
	stored := map[string]bool{}
	f.handle("PUT "+path, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if stored[oid] {
			w.WriteHeader(200)
			return
		}
		stored[oid] = true
		w.WriteHeader(201)
	})

	// The body is a plain io.Reader (not bytes.Reader) so nothing can
	// rewind or pre-size it.
	created, err := c.PutObject(ctx, 3, wire.ObjectKindData, oid, io.MultiReader(bytes.NewReader(data)), int64(len(data)), "")
	if err != nil || !created {
		t.Fatalf("put: created=%v err=%v", created, err)
	}
	got := f.last()
	if got.Header.Get(protocol.HeaderBodySHA256) != oid || got.Length != int64(len(data)) || len(got.Header["Transfer-Encoding"]) != 0 {
		t.Fatalf("headers %v length %d", got.Header, got.Length)
	}
	if !bytes.Equal(got.Body, data) {
		t.Fatal("body differs")
	}
	if created, err = c.PutObject(ctx, 3, wire.ObjectKindData, oid, bytes.NewReader(data), int64(len(data)), ""); err != nil || created {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}

	// A body that does not hash to the oid aborts before the request completes.
	n := len(f.reqs)
	bad := append([]byte{}, data...)
	bad[10] ^= 1
	if _, err := c.PutObject(ctx, 3, wire.ObjectKindData, oid, bytes.NewReader(bad), int64(len(bad)), ""); !errors.Is(err, ErrBodyMismatch) {
		t.Fatalf("tampered body: %v", err)
	}
	// A short body is rejected too.
	if _, err := c.PutObject(ctx, 3, wire.ObjectKindData, oid, bytes.NewReader(data[:100]), int64(len(data)), ""); !errors.Is(err, ErrBodyMismatch) {
		t.Fatalf("short body: %v", err)
	}
	if len(f.reqs) != n {
		t.Fatal("server accepted an aborted upload")
	}
	// config needs an explicit hash.
	if _, err := c.PutObject(ctx, 3, wire.ObjectKindConfig, wire.ObjectConfigOID, bytes.NewReader(data), int64(len(data)), ""); err == nil {
		t.Fatal("config without hash accepted")
	}
}

func TestHeadGetObject(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	oid := strings.Repeat("ab", 32)
	f.handle("HEAD /api/v1/repos/1/objects/index/"+oid, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Length", "42")
		w.WriteHeader(200)
	})
	f.handle("GET /api/v1/repos/1/objects/index/"+oid, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Write([]byte(r.Header.Get("Range")))
	})
	ok, size, err := c.HeadObject(ctx, 1, wire.ObjectKindIndex, oid)
	if err != nil || !ok || size != 42 {
		t.Fatalf("head: %v %d %v", ok, size, err)
	}
	if ok, _, err := c.HeadObject(ctx, 1, wire.ObjectKindIndex, strings.Repeat("cd", 32)); err != nil || ok {
		t.Fatalf("head missing: %v %v", ok, err)
	}
	rc, err := c.GetObject(ctx, 1, wire.ObjectKindIndex, oid, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "bytes=10-14" {
		t.Fatalf("range %q", b)
	}
	if _, err := c.GetObject(ctx, 1, wire.ObjectKindIndex, strings.Repeat("cd", 32), 0, 0); !IsCode(err, "not_found") {
		t.Fatalf("missing get: %v", err)
	}
}

func TestReposAndObjectList(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("POST /api/v1/repos", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.RepoCreateRequest
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.FormatVersion != 1 {
			writeErr(w, 400, "invalid", nil)
			return
		}
		writeJSON(w, 201, wire.Repo{ID: 4, RepoUUID: req.RepoUUID, Name: req.Name, FormatVersion: 1, EscrowMode: wire.EscrowModeFull})
	})
	f.handle("GET /api/v1/repos/4", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, wire.Repo{ID: 4})
	})
	f.handle("GET /api/v1/repos/4/objects", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("kind") != "data" || r.URL.Query().Get("cursor") != "c1" {
			writeErr(w, 400, "bad_query", nil)
			return
		}
		writeJSON(w, 200, wire.List[wire.ObjectInfo]{Items: []wire.ObjectInfo{{OID: "x", Size: 3}}})
	})
	f.handle("POST /api/v1/repos/4/objects/missing", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.ObjectMissingRequest
		json.Unmarshal(body, &req)
		writeJSON(w, 200, wire.ObjectMissingResponse{Missing: req.OIDs[:1]})
	})
	repo, err := c.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: "u", Name: "PC", DestinationID: 1})
	if err != nil || repo.ID != 4 {
		t.Fatalf("create: %+v %v", repo, err)
	}
	if r, err := c.Repo(ctx, 4); err != nil || r.ID != 4 {
		t.Fatalf("get: %v", err)
	}
	l, err := c.ListObjects(ctx, 4, wire.ObjectKindData, "c1", 0)
	if err != nil || len(l.Items) != 1 || l.Items[0].Size != 3 {
		t.Fatalf("list: %+v %v", l, err)
	}
	oids := make([]string, wire.ObjectMissingMaxOIDs+5)
	for i := range oids {
		oids[i] = "o"
	}
	miss, err := c.MissingObjects(ctx, 4, wire.ObjectKindData, oids)
	if err != nil || len(miss) != 2 {
		t.Fatalf("missing batches: %d %v", len(miss), err)
	}
}

func TestLocksAndRepoLocked(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("POST /api/v1/repos/2/locks", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.LockRequest
		json.Unmarshal(body, &req)
		if req.Kind == wire.LockKindExclusive {
			writeErr(w, 409, wire.CodeRepoLocked, wire.LockedDetails{Holder: "device 9", ExpiresAt: 123})
			return
		}
		writeJSON(w, 201, wire.Lock{LockID: "L1", ExpiresAt: 99})
	})
	f.handle("PUT /api/v1/repos/2/locks/L1", func(w http.ResponseWriter, r *http.Request, body []byte) {
		if string(body) != `{"ttl_seconds":60}` {
			writeErr(w, 400, "bad_body", nil)
			return
		}
		w.WriteHeader(204)
	})
	f.handle("DELETE /api/v1/repos/2/locks/L1", func(w http.ResponseWriter, r *http.Request, _ []byte) { w.WriteHeader(204) })

	l, err := c.AcquireLock(ctx, 2, wire.LockKindShared, 300)
	if err != nil || l.LockID != "L1" {
		t.Fatalf("acquire: %+v %v", l, err)
	}
	if l, err := c.RefreshLock(ctx, 2, "L1", 60); err != nil || l != nil {
		t.Fatalf("refresh: %+v %v", l, err)
	}
	if err := c.ReleaseLock(ctx, 2, "L1"); err != nil {
		t.Fatal(err)
	}
	_, err = c.AcquireLock(ctx, 2, wire.LockKindExclusive, 300)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != wire.CodeRepoLocked || ae.Message == "" {
		t.Fatalf("locked: %#v", err)
	}
	d, ok := RepoLocked(err)
	if !ok || d.Holder != "device 9" || d.ExpiresAt != 123 {
		t.Fatalf("details: %+v %v", d, ok)
	}
	if _, ok := ModuleUnavailable(err); ok {
		t.Fatal("repo_locked decoded as module_unavailable")
	}
}

func TestCommitSnapshotStreamed(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	seen := map[string]bool{}
	f.handle("POST /api/v1/repos/5/snapshots", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.SnapshotCommitRequest
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeErr(w, 400, "invalid", nil)
			return
		}
		st := 201
		if seen[req.SnapshotOID] {
			st = 200
		}
		seen[req.SnapshotOID] = true
		writeJSON(w, st, wire.SnapshotCommitResponse{SnapshotID: 77})
	})
	req := wire.SnapshotCommitRequest{SnapshotOID: strings.Repeat("1", 64), JobID: 1, RunID: 2, Kind: "folder", Mode: "full", Tags: []string{}, Epoch: 1, ProfileConsentID: "c"}
	for i := 0; i < 40000; i++ {
		req.PackOIDs = append(req.PackOIDs, strings.Repeat("f", 64))
	}
	resp, created, err := c.CommitSnapshot(ctx, 5, req)
	if err != nil || !created || resp.SnapshotID != 77 {
		t.Fatalf("commit: %+v %v %v", resp, created, err)
	}
	if got := f.last(); len(got.Body) < 2<<20 {
		t.Fatalf("expected a body over the 2 MiB signed limit, got %d", len(got.Body))
	}
	if _, created, err = c.CommitSnapshot(ctx, 5, req); err != nil || created {
		t.Fatalf("replay: %v %v", created, err)
	}
}

func TestAPIErrorAndAccepted(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("POST /api/v1/backup/jobs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Retry-After", "3")
		writeErr(w, 409, wire.CodeModuleUnavailable, ModuleUnavailableDetails{Module: wire.ModuleVSS, DeviceID: 7})
	})
	f.handle("POST /api/v1/op", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 202, map[string]any{"operation_id": 11})
	})
	f.handle("POST /api/v1/appr", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 202, map[string]any{"approval_id": 5, "operation_id": nil})
	})
	_, err := c.CreateJob(ctx, wire.JobCreateRequest{Name: "x"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != wire.CodeModuleUnavailable || ae.RetryAfter != 3*time.Second {
		t.Fatalf("err %#v", err)
	}
	d, ok := ModuleUnavailable(err)
	if !ok || d.Module != wire.ModuleVSS || d.DeviceID != 7 {
		t.Fatalf("details %+v", d)
	}
	if b := f.last().Body; !bytes.Contains(b, []byte(`"device_id":7`)) {
		t.Fatalf("device id default not applied: %s", b)
	}

	a, err := c.CallAccepted(ctx, "POST", "/api/v1/op", nil)
	if err != nil || a.Operation == nil || a.Operation.OperationID != 11 || a.Approval != nil {
		t.Fatalf("operation: %+v %v", a, err)
	}
	a, err = c.CallAccepted(ctx, "POST", "/api/v1/appr", nil)
	if err != nil || a.Approval == nil || a.Approval.ApprovalID != 5 || a.Approval.OperationID != nil {
		t.Fatalf("approval: %+v %v", a, err)
	}
	if _, err := ParseAccepted([]byte(`{}`)); err == nil {
		t.Fatal("empty 202 accepted")
	}
}

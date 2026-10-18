package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

const testRepoID = 42

type fakeLock struct {
	kind    string
	device  string
	expires time.Time
}

// fakeServer is an in-memory api-v1 §5 server for one repository: objects
// with Range, paged lists (page size 2), shared/exclusive locks with
// repo_locked details, the exclusive-lock rule for config PUT, the body
// hash checks and the snapshot commit route, plus fault injection.
type fakeServer struct {
	url string

	mu        sync.Mutex
	objects   map[string]map[string][]byte
	locks     map[string]fakeLock
	snapshots map[string]int64
	nextLock  int

	// Fault injection: the next fail503[route] requests of a route
	// ("METHOD kind" for objects, else "METHOD /path-tail") get 503.
	fail503  map[string]int
	hash422  bool // the next object PUT gets 422 object_hash_mismatch
	noRange  bool // ignore Range headers
	requests int
}

func newFake(t *testing.T) (*fakeServer, *client.Client) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{objects: map[string]map[string][]byte{}, locks: map[string]fakeLock{},
		snapshots: map[string]int64{}, fail503: map[string]int{}}
	srv := httptest.NewServer(f.mux())
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f, f.client(t, srv.URL, 7, priv)
}

// client returns a client for another device of the same server.
func (f *fakeServer) client(t *testing.T, raw string, device int64, priv ed25519.PrivateKey) *client.Client {
	t.Helper()
	base, _ := url.Parse(raw)
	c := client.New(base, client.Options{Timeout: 10 * time.Second})
	c.DeviceID, c.Key = device, priv
	return c
}

func writeErr(w http.ResponseWriter, status int, code string, details any) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": code, "details": details}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeServer) fault(key string) bool {
	f.requests++
	if f.fail503[key] > 0 {
		f.fail503[key]--
		return true
	}
	return false
}

// body reads the request body and checks it against the signed hash.
func (f *fakeServer) body(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, "bad_body", nil)
		return nil, false
	}
	if protocol.SHA256Hex(b) != r.Header.Get(protocol.HeaderBodySHA256) {
		writeErr(w, 400, "body_mismatch", nil)
		return nil, false
	}
	return b, true
}

func (f *fakeServer) checkRepo(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("id") != strconv.Itoa(testRepoID) {
		writeErr(w, 404, wire.CodeRepoNotFound, nil)
		return false
	}
	return true
}

func validKind(k string) bool {
	return slices.Contains([]string{"config", "keys", "chain", "index", "snapshots", "data", "locks"}, k)
}

// expire drops expired locks; f.mu is held.
func (f *fakeServer) expire() {
	for id, l := range f.locks {
		if !time.Now().Before(l.expires) {
			delete(f.locks, id)
		}
	}
}

func (f *fakeServer) mux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/repos/{id}/objects/{kind}/{oid}", f.getObject) // also HEAD
	m.HandleFunc("PUT /api/v1/repos/{id}/objects/{kind}/{oid}", f.putObject)
	m.HandleFunc("GET /api/v1/repos/{id}/objects", f.listObjects)
	m.HandleFunc("POST /api/v1/repos/{id}/locks", f.acquire)
	m.HandleFunc("PUT /api/v1/repos/{id}/locks/{lock}", f.refresh)
	m.HandleFunc("DELETE /api/v1/repos/{id}/locks/{lock}", f.release)
	m.HandleFunc("POST /api/v1/repos/{id}/snapshots", f.commit)
	return m
}

func (f *fakeServer) getObject(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	kind, oid := r.PathValue("kind"), r.PathValue("oid")
	if f.fault(r.Method + " " + kind) {
		writeErr(w, 503, "capacity", nil)
		return
	}
	if !validKind(kind) || len(oid) != 64 {
		writeErr(w, 400, wire.CodeInvalidOID, nil)
		return
	}
	b, ok := f.objects[kind][oid]
	if !ok {
		if r.Method == http.MethodHead {
			w.WriteHeader(404)
			return
		}
		writeErr(w, 404, wire.CodeObjectNotFound, nil)
		return
	}
	if f.noRange {
		r.Header.Del("Range")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
}

func (f *fakeServer) putObject(w http.ResponseWriter, r *http.Request) {
	body, ok := f.body(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	kind, oid := r.PathValue("kind"), r.PathValue("oid")
	if f.fault("PUT " + kind) {
		writeErr(w, 503, "capacity", nil)
		return
	}
	if !validKind(kind) || len(oid) != 64 || (kind == "config") != (oid == wire.ObjectConfigOID) {
		writeErr(w, 400, wire.CodeInvalidOID, nil)
		return
	}
	sum := sha256.Sum256(body)
	if f.hash422 || (kind != "config" && hex.EncodeToString(sum[:]) != oid) {
		f.hash422 = false
		writeErr(w, 422, wire.CodeObjectHashMismatch, nil)
		return
	}
	if kind == "config" {
		f.expire()
		dev := r.Header.Get(protocol.HeaderDevice)
		held := false
		for _, l := range f.locks {
			held = held || (l.kind == wire.LockKindExclusive && l.device == dev)
		}
		if !held {
			writeErr(w, 409, wire.CodeRepoLocked, wire.LockedDetails{Holder: "nobody"})
			return
		}
	}
	if f.objects[kind] == nil {
		f.objects[kind] = map[string][]byte{}
	}
	old, exists := f.objects[kind][oid]
	if exists && bytes.Equal(old, body) {
		w.WriteHeader(200)
		return
	}
	f.objects[kind][oid] = body
	w.WriteHeader(201)
}

func (f *fakeServer) listObjects(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if f.fault("LIST " + kind) {
		writeErr(w, 503, "capacity", nil)
		return
	}
	oids := make([]string, 0, len(f.objects[kind]))
	for oid := range f.objects[kind] {
		oids = append(oids, oid)
	}
	slices.Sort(oids)
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(start+2, len(oids))
	out := wire.List[wire.ObjectInfo]{Items: []wire.ObjectInfo{}}
	for _, oid := range oids[min(start, end):end] {
		out.Items = append(out.Items, wire.ObjectInfo{OID: oid, Size: int64(len(f.objects[kind][oid]))})
	}
	if end < len(oids) {
		out.NextCursor = strconv.Itoa(end)
	}
	writeJSON(w, 200, out)
}

func (f *fakeServer) acquire(w http.ResponseWriter, r *http.Request) {
	body, ok := f.body(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	if f.fault("POST /locks") {
		writeErr(w, 503, "capacity", nil)
		return
	}
	var req wire.LockRequest
	if json.Unmarshal(body, &req) != nil || (req.Kind != wire.LockKindShared && req.Kind != wire.LockKindExclusive) || req.TTLSeconds <= 0 {
		writeErr(w, 400, "validation", nil)
		return
	}
	f.expire()
	for _, l := range f.locks {
		if req.Kind == wire.LockKindExclusive || l.kind == wire.LockKindExclusive {
			writeErr(w, 409, wire.CodeRepoLocked, wire.LockedDetails{Holder: "device " + l.device, ExpiresAt: l.expires.UnixMilli()})
			return
		}
	}
	f.nextLock++
	id := fmt.Sprintf("lk%d", f.nextLock)
	l := fakeLock{kind: req.Kind, device: r.Header.Get(protocol.HeaderDevice), expires: time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)}
	f.locks[id] = l
	writeJSON(w, 201, wire.Lock{LockID: id, ExpiresAt: l.expires.UnixMilli()})
}

func (f *fakeServer) refresh(w http.ResponseWriter, r *http.Request) {
	body, ok := f.body(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	var req struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	_ = json.Unmarshal(body, &req)
	f.expire()
	id := r.PathValue("lock")
	l, ok := f.locks[id]
	if !ok {
		writeErr(w, 404, wire.CodeLockNotFound, nil)
		return
	}
	l.expires = time.Now().Add(time.Duration(max(req.TTLSeconds, 1)) * time.Second)
	f.locks[id] = l
	writeJSON(w, 200, wire.Lock{LockID: id, ExpiresAt: l.expires.UnixMilli()})
}

func (f *fakeServer) release(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	f.expire()
	id := r.PathValue("lock")
	if _, ok := f.locks[id]; !ok {
		writeErr(w, 404, wire.CodeLockNotFound, nil)
		return
	}
	delete(f.locks, id)
	w.WriteHeader(204)
}

func (f *fakeServer) commit(w http.ResponseWriter, r *http.Request) {
	body, ok := f.body(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.checkRepo(w, r) {
		return
	}
	if f.fault("POST /snapshots") {
		writeErr(w, 503, "capacity", nil)
		return
	}
	var req wire.SnapshotCommitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, 400, "validation", nil)
		return
	}
	if id, ok := f.snapshots[req.SnapshotOID]; ok {
		writeJSON(w, 200, wire.SnapshotCommitResponse{SnapshotID: id})
		return
	}
	var missing []string
	if _, ok := f.objects["snapshots"][req.SnapshotOID]; !ok {
		missing = append(missing, req.SnapshotOID)
	}
	for _, p := range req.PackOIDs {
		if _, ok := f.objects["data"][p]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		writeErr(w, 409, wire.CodePackMissing, map[string]any{"missing": missing})
		return
	}
	id := int64(len(f.snapshots) + 1)
	f.snapshots[req.SnapshotOID] = id
	writeJSON(w, 201, wire.SnapshotCommitResponse{SnapshotID: id})
}

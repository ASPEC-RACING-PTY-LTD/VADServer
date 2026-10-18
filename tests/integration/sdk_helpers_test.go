package integration

// Helpers for the in-process SDK flows (keys_test.go, backup_test.go). They
// run against internal/testserver (a fresh server per test, with a
// generated KEK and the catalog worker running), so unlike
// integration_test.go they need no deployed instance and no build tag.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	serverbe "github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo/backend/server"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/testserver"
)

const sdkProfileSID = "S-1-5-21-1-2-3-1001"

// keyDevice is a device enrolled with its X25519 box key (api-v1 §5A).
type keyDevice struct {
	name    string
	c       *client.Client
	sign    ed25519.PrivateKey
	signPub ed25519.PublicKey
	boxPriv vdcrypt.Key
	boxPub  vdcrypt.Key
	ident   *wire.ServerIdentity // server_identity from the approved poll
}

// enrolKeyDevice enrols a device for the test server's admin with box_pub,
// approves it with the admin session and polls the approved status.
func enrolKeyDevice(t *testing.T, srv *testserver.Server, name string) *keyDevice {
	t.Helper()
	base, _ := url.Parse(srv.URL)
	c := client.New(base, client.Options{})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	boxPriv, boxPub, err := vdcrypt.GenerateX25519(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := c.Enroll(ctx, client.EnrollRequest{Login: "admin", Password: testserver.Password, DeviceName: name, OS: "Windows 11",
		Identifier: name, PublicKey: protocol.EncodePublicKey(pub), ClientVersion: "itest",
		BoxPub: base64.StdEncoding.EncodeToString(boxPub[:]), InstallMode: "simple", CoreVersion: "itest"})
	if err != nil {
		t.Fatalf("enrol %s: %v", name, err)
	}
	srv.Do("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil, 200)
	st, err := c.EnrollStatus(ctx, res.RequestID, res.PollToken)
	if err != nil || st.Status != "approved" {
		t.Fatalf("enrol %s status: %+v %v", name, st, err)
	}
	c.DeviceID, c.Key = st.DeviceID, priv
	return &keyDevice{name: name, c: c, sign: priv, signPub: pub, boxPriv: boxPriv, boxPub: boxPub, ident: st.ServerIdentity}
}

// sikFingerprint is api-v1 §14 rev 1.1b: uppercase hex of SHA-256(sik_pub)
// [:16] in groups of 4 joined by "-". Computed independently of the server
// and the desktop keystore.
func sikFingerprint(sik ed25519.PublicKey) string {
	sum := sha256.Sum256(sik)
	h := strings.ToUpper(hex.EncodeToString(sum[:16]))
	var g []string
	for i := 0; i < len(h); i += 4 {
		g = append(g, h[i:i+4])
	}
	return strings.Join(g, "-")
}

// checkedIdentity decodes a server identity, recomputes the fingerprint and
// verifies the SIK signature over the SEK.
type checkedIdentity struct {
	sik ed25519.PublicKey
	sek vdcrypt.Key
	sig []byte
	ver uint32
	fp  string
}

func checkIdentity(t *testing.T, id *wire.ServerIdentity) checkedIdentity {
	t.Helper()
	if id == nil {
		t.Fatal("no server identity")
	}
	sik, err := base64.StdEncoding.DecodeString(id.SIKPub)
	if err != nil || len(sik) != ed25519.PublicKeySize {
		t.Fatalf("sik_pub: %v", err)
	}
	sekRaw, err := base64.StdEncoding.DecodeString(id.SEKPub)
	if err != nil {
		t.Fatalf("sek_pub: %v", err)
	}
	sek, err := vdcrypt.KeyFromBytes(sekRaw)
	if err != nil {
		t.Fatalf("sek_pub: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(id.SEKSig)
	if err != nil {
		t.Fatalf("sek_sig: %v", err)
	}
	fp := sikFingerprint(sik)
	if fp != id.SIKFingerprint {
		t.Fatalf("fingerprint %s, server says %s", fp, id.SIKFingerprint)
	}
	if !vdcrypt.VerifySEK(sik, sek, id.SEKVersion, sig) {
		t.Fatal("SEK signature does not verify under the SIK")
	}
	return checkedIdentity{sik: sik, sek: sek, sig: sig, ver: id.SEKVersion, fp: fp}
}

func (ci checkedIdentity) escrowRecipient() repo.Recipient {
	return repo.EscrowRecipient(ci.sek, ci.sik, ci.sig, ci.ver, "server")
}

func (d *keyDevice) recipient() repo.Recipient {
	return repo.DeviceRecipient(d.boxPub, d.c.DeviceID, d.signPub, d.name)
}

func (d *keyDevice) backend(repoID int64) repo.Backend {
	return serverbe.New(d.c, repoID, serverbe.Options{})
}

// initRepo creates a server repository and initialises it over the server
// backend (first-device keys: device, escrow and recovery wraps).
func initRepo(t *testing.T, d *keyDevice, ci checkedIdentity, uuid, name, escrow string) (int64, *repo.Repo, string) {
	t.Helper()
	ctx := context.Background()
	rp, err := d.c.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: uuid, Name: name, EscrowMode: escrow})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	rid, err := vdcrypt.ParseRepoID(uuid)
	if err != nil {
		t.Fatal(err)
	}
	r, recovery, err := repo.Init(ctx, d.backend(rp.ID), repo.InitOptions{RepoID: rid, EscrowMode: escrow,
		Recipients: []repo.Recipient{d.recipient(), ci.escrowRecipient()}})
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	t.Cleanup(r.Close)
	return rp.ID, r, recovery
}

// openRepo opens the repository with the device's box key.
func (d *keyDevice) openRepo(t *testing.T, repoID int64) *repo.Repo {
	t.Helper()
	r, err := repo.Open(context.Background(), d.backend(repoID), repo.DeviceCredential(d.boxPriv), repo.Options{})
	if err != nil {
		t.Fatalf("%s: open repo %d: %v", d.name, repoID, err)
	}
	t.Cleanup(r.Close)
	return r
}

// keyTask returns the first pending task of kind whose payload satisfies
// match (nil = any). Tasks that do not match are left claimed.
func (d *keyDevice) keyTask(t *testing.T, kind string, match func(wire.DeviceTask) bool) wire.DeviceTask {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := d.c.PollTasks(context.Background(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.Kind == kind && (match == nil || match(task)) {
				return task
			}
		}
	}
	t.Fatalf("%s: no %s task", d.name, kind)
	return wire.DeviceTask{}
}

// reportConsent records an active_default profile with consent "c1", which
// snapshot commits require (api-v1 §6B).
func reportConsent(t *testing.T, d *keyDevice) {
	t.Helper()
	rep := wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: sdkProfileSID, Username: "u", ProfilePath: `C:\Users\u`, State: wire.ProfileStateActiveDefault}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: sdkProfileSID, ConsentAt: 1000, ProofDigest: strings.Repeat("a", 64)}},
	}
	if err := d.c.ReportProfiles(context.Background(), rep); err != nil {
		t.Fatalf("profile report: %v", err)
	}
}

// snapWriter writes snapshots of an in-memory file set ({DOCUMENTS}-relative
// slash paths) the way the backup pipeline does: one blob per file content
// (deduplicated against the repository index), trees in a meta pack, the
// index, then the signed root. It remembers which pack holds each blob so
// commits can list every referenced pack (api-v1 §5.2).
type snapWriter struct {
	t      *testing.T
	r      *repo.Repo
	dev    *keyDevice
	blobAt map[pack.BlobID]string // blob id → pack oid
}

type snapResult struct {
	oid      string
	packs    []string // every pack the snapshot references
	newPacks []string
	newBlobs int
	newBytes int64
	files    int64
	bytes    int64
}

const sdkMTime = int64(1760745600000000000)

func (w *snapWriter) write(files map[string][]byte, parent *string) snapResult {
	t := w.t
	t.Helper()
	ctx := context.Background()
	if w.blobAt == nil {
		w.blobAt = map[pack.BlobID]string{}
	}
	rk, err := w.r.Keys()
	if err != nil {
		t.Fatal(err)
	}
	var res snapResult
	dw, err := w.r.NewPackWriter(pack.ClassData)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := w.r.NewPackWriter(pack.ClassMeta)
	if err != nil {
		t.Fatal(err)
	}
	var pendingData, pendingMeta []pack.BlobID
	refs := map[pack.BlobID]bool{}
	add := func(pw *pack.Writer, pending *[]pack.BlobID, typ pack.BlobType, raw []byte) pack.BlobID {
		id := pack.ComputeID(rk.ID, raw)
		refs[id] = true
		if w.r.HasBlob(id) || pw.Has(id) {
			return id
		}
		if _, err := pw.Add(typ, raw, false); err != nil {
			t.Fatalf("pack add: %v", err)
		}
		*pending = append(*pending, id)
		if typ == pack.TypeData {
			res.newBlobs++
			res.newBytes += int64(len(raw))
		}
		return id
	}

	// Group the files by directory and build the trees bottom-up.
	dirs := map[string][]snapshot.Entry{"": nil}
	var paths []string
	for p := range files {
		paths = append(paths, p)
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if _, ok := dirs[d]; !ok {
				dirs[d] = nil
			}
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		data := files[p]
		e := snapshot.Entry{N: path.Base(p), T: snapshot.TypeFile, S: int64(len(data)), M: sdkMTime + int64(len(data)), A: 32, Ch: []string{}}
		if len(data) > 0 {
			e.Ch = []string{add(dw, &pendingData, pack.TypeData, data).String()}
		}
		h := snapshot.FileHashBytes(rk.FileHash, data)
		e.H = snapshot.HexPtr(hex.EncodeToString(h[:]))
		d := path.Dir(p)
		if d == "." {
			d = ""
		}
		dirs[d] = append(dirs[d], e)
		res.files++
		res.bytes += int64(len(data))
	}
	var dirNames []string
	for d := range dirs {
		dirNames = append(dirNames, d)
	}
	// Deepest first, so every subtree id exists before its parent is encoded.
	sort.Slice(dirNames, func(i, j int) bool {
		di, dj := strings.Count(dirNames[i], "/"), strings.Count(dirNames[j], "/")
		if di != dj {
			return di > dj
		}
		return dirNames[i] < dirNames[j]
	})
	var rootTree string
	for _, d := range dirNames {
		if d == "" {
			continue
		}
		tbs, err := snapshot.EncodeTrees(rk.ID, dirs[d])
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range tbs {
			add(mw, &pendingMeta, pack.TypeTree, tb.Raw)
		}
		parentDir := path.Dir(d)
		if parentDir == "." {
			parentDir = ""
		}
		dirs[parentDir] = append(dirs[parentDir], snapshot.Entry{N: path.Base(d), T: snapshot.TypeDir, M: sdkMTime, A: 16, Sub: snapshot.HexPtr(tbs[0].IDHex())})
	}
	tbs, err := snapshot.EncodeTrees(rk.ID, dirs[""])
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tbs {
		add(mw, &pendingMeta, pack.TypeTree, tb.Raw)
	}
	rootTree = tbs[0].IDHex()

	for _, pw := range []struct {
		w       *pack.Writer
		pending []pack.BlobID
	}{{dw, pendingData}, {mw, pendingMeta}} {
		if pw.w.Len() == 0 {
			continue
		}
		p, err := pw.w.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if err := w.r.SavePack(ctx, p); err != nil {
			t.Fatalf("save pack: %v", err)
		}
		for _, id := range pw.pending {
			w.blobAt[id] = p.OID
		}
		res.newPacks = append(res.newPacks, p.OID)
	}
	if _, err := w.r.FlushIndex(ctx); err != nil {
		t.Fatalf("flush index: %v", err)
	}
	seen := map[string]bool{}
	for id := range refs {
		oid, ok := w.blobAt[id]
		if !ok {
			t.Fatalf("blob %s has no known pack", id)
		}
		if !seen[oid] {
			seen[oid] = true
			res.packs = append(res.packs, oid)
		}
	}
	sort.Strings(res.packs)

	root := &snapshot.Root{V: snapshot.Version, Time: time.Now().UnixMilli(), Parent: parent,
		Job:     snapshot.Job{Name: "Documents", Kind: wire.JobKindFolder, Mode: wire.JobModeFull},
		Device:  snapshot.Device{ID: w.dev.c.DeviceID, Name: w.dev.name},
		Host:    snapshot.Host{Hostname: w.dev.name, OS: "windows", User: "u", SID: sdkProfileSID, MachineGUIDHash: strings.Repeat("ab", 32)},
		Tokens:  snapshot.Tokens{snapshot.TokenDocuments: `C:\Users\u\Documents`},
		Roots:   []snapshot.RootRef{{TokenPath: snapshot.TokenDocuments, Tree: rootTree}},
		Stats:   snapshot.Stats{Files: res.files, Dirs: int64(len(dirs)), Bytes: res.bytes},
		Profile: snapshot.Profile{SID: sdkProfileSID, Username: "u", ConsentID: "c1"}, InstallMode: snapshot.InstallModeSimple}
	if res.oid, err = w.r.SaveSnapshot(ctx, root, w.dev.sign); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	return res
}

// commit registers a written snapshot in the server catalog.
func (w *snapWriter) commit(repoID int64, s snapResult, parentID *int64) (int64, bool) {
	w.t.Helper()
	resp, created, err := w.dev.c.CommitSnapshot(context.Background(), repoID, wire.SnapshotCommitRequest{
		SnapshotOID: s.oid, ParentSnapshotID: parentID, Kind: wire.JobKindFolder, Mode: wire.JobModeFull, Hostname: w.dev.name,
		OSUser: "u", UserSID: sdkProfileSID, ClientTime: time.Now().UnixMilli(), IndexOIDs: w.r.Index().Files(), PackOIDs: s.packs,
		Epoch: int64(w.r.Epoch()), ProfileConsentID: "c1",
		Stats: wire.SnapshotStats{FileCount: s.files, LogicalBytes: s.bytes, NewBytes: s.newBytes, NewObjects: int64(len(s.newPacks))}})
	if err != nil {
		w.t.Fatalf("commit: %v", err)
	}
	return resp.SnapshotID, created
}

// restoreAll reads every file of a snapshot back through r and checks each
// file's keyed hash and size.
func restoreAll(t *testing.T, r *repo.Repo, oid string) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	root, stored, err := r.LoadSnapshot(ctx, oid)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if err := r.VerifySnapshot(root, stored); err != nil {
		t.Fatalf("verify snapshot: %v", err)
	}
	rk, err := r.Keys()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	var walk func(prefix, tree string)
	walk = func(prefix, tree string) {
		for tree != "" {
			tr, err := r.ReadTree(ctx, tree)
			if err != nil {
				t.Fatalf("tree %s: %v", prefix, err)
			}
			for _, e := range tr.Entries {
				p := strings.TrimPrefix(prefix+"/"+e.N, "/")
				if e.T == snapshot.TypeDir {
					walk(p, *e.Sub)
					continue
				}
				var buf []byte
				for _, c := range e.Ch {
					raw, _ := hex.DecodeString(c)
					b, err := r.ReadBlob(ctx, pack.BlobID(raw))
					if err != nil {
						t.Fatalf("blob of %s: %v", p, err)
					}
					buf = append(buf, b...)
				}
				if !snapshot.VerifyFileHash(snapshot.FileHashBytes(rk.FileHash, buf), *e.H) || int64(len(buf)) != e.S {
					t.Errorf("%s: hash or size mismatch", p)
				}
				out[p] = buf
			}
			tree = ""
			if tr.Cont != nil {
				tree = *tr.Cont
			}
		}
	}
	for _, rr := range root.Roots {
		walk("", rr.Tree)
	}
	return out
}

// waitIngest waits for the catalog worker to finish a snapshot.
func waitIngest(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(20 * time.Second)
	for {
		sn, err := st.GetSnapshot(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		switch sn.IngestState {
		case wire.SnapshotIngestDone:
			return
		case wire.SnapshotIngestFailed:
			msg, _ := st.SnapshotIngestError(ctx, id)
			t.Fatalf("snapshot %d ingest failed: %s", id, msg)
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot %d still %s", id, sn.IngestState)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// itemNames returns the "name" of each item of a List reply.
func itemNames(out map[string]any) []string {
	var ns []string
	items, _ := out["items"].([]any)
	for _, it := range items {
		ns = append(ns, it.(map[string]any)["name"].(string))
	}
	return ns
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

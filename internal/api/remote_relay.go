package api

// Reading and moving files held by remote storage nodes.
//
// storedFile is what every reader of stored content gets: the local file,
// or a remoteFile that fetches only the encrypted chunks it needs from the
// node (signed range requests to its public address), checks and decrypts
// them in memory and keeps a few in a small shared cache for seeking.
// Nothing is written to disk. FFmpeg reads remote files through a loopback
// URL served by this process (127.0.0.1 only, one-time tokens).
//
// Moving a user's files to their node reuses the copy jobs: the node
// downloads the file encrypted for its key pair (VDR2), reports the
// checksum of what it stored, and only when that matches is the local file
// removed.

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/replica"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	relayCacheBytes = 64 << 20 // decrypted chunks kept in memory
	relayWindow     = 4        // chunks fetched per request when reading on
	moveAttempts    = 5
)

// storedFile is opened stored content.
type storedFile interface {
	io.ReadSeekCloser
	Size() int64
	// Media is the input FFmpeg reads the content from; release it after.
	Media() (in media.Input, release func())
}

type localFile struct{ *os.File }

func (f localFile) Size() int64 {
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return 0
}

func (f localFile) Media() (media.Input, func()) { return media.File(f.File), func() {} }

// openBlob opens a user's stored content wherever it is.
func (s *Server) openBlob(ctx context.Context, userID int64, sha string) (storedFile, error) {
	if rb, err := s.st.RemoteBlob(ctx, userID, sha); err == nil && rb.State == "ok" {
		return s.openRemote(ctx, rb)
	}
	loc, err := s.st.BlobLocation(ctx, userID, sha)
	if err != nil {
		return nil, err
	}
	f, err := s.blobs.Open(loc, userID, sha)
	if err != nil {
		return nil, err
	}
	return localFile{f}, nil
}

// --- the remote service ---

type remoteService struct {
	http  *http.Client
	cache *chunkCache

	mu      sync.Mutex
	heads   map[string]*remoteHead // user/sha -> parsed header and file key
	moves   map[int64]*moveState   // by user
	tokens  map[string]relayToken  // loopback tokens
	lnOnce  sync.Once
	lnAddr  string
	lnError error
}

type remoteHead struct {
	h   *replica.V2Header
	key []byte
}

type relayToken struct {
	userID  int64
	sha     string
	expires time.Time
}

// moveState is the progress of moving a user's files to their node.
type moveState struct {
	NodeID  int64  `json:"node_id"`
	Running bool   `json:"running"`
	Moved   int    `json:"moved"`
	Failed  int    `json:"failed"`
	Bytes   int64  `json:"bytes"`
	Error   string `json:"error,omitempty"`
	Started int64  `json:"started_at"`
	Ended   int64  `json:"ended_at,omitempty"`
}

var remoteServices sync.Map // *Server -> *remoteService

func (s *Server) remote() *remoteService {
	v, _ := remoteServices.LoadOrStore(s, &remoteService{
		http:   &http.Client{Timeout: 2 * time.Minute},
		cache:  newChunkCache(relayCacheBytes),
		heads:  map[string]*remoteHead{},
		moves:  map[int64]*moveState{},
		tokens: map[string]relayToken{},
	})
	return v.(*remoteService)
}

// moving lists the moves to a node (running or finished), by user.
func (rs *remoteService) moving(nodeID int64) map[int64]moveState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := map[int64]moveState{}
	for u, m := range rs.moves {
		if m.NodeID == nodeID {
			out[u] = *m
		}
	}
	return out
}

// --- remote reads ---

// remoteFile reads a remote file through the relay.
type remoteFile struct {
	s       *Server
	rb      store.RemoteBlob
	head    *remoteHead
	pos     int64
	cacheID string
}

func (s *Server) openRemote(ctx context.Context, rb *store.RemoteBlob) (*remoteFile, error) {
	head, err := s.remoteHeader(ctx, rb)
	if err != nil {
		return nil, err
	}
	return &remoteFile{s: s, rb: *rb, head: head, cacheID: fmt.Sprintf("%d/%s/", rb.UserID, rb.SHA256)}, nil
}

func (f *remoteFile) Size() int64  { return f.head.h.Size }
func (f *remoteFile) Close() error { return nil }

func (f *remoteFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += f.pos
	case io.SeekEnd:
		off += f.head.h.Size
	}
	if off < 0 {
		return 0, errors.New("seek before start")
	}
	f.pos = off
	return off, nil
}

func (f *remoteFile) Read(p []byte) (int, error) {
	if f.pos >= f.head.h.Size {
		return 0, io.EOF
	}
	i := f.pos / replica.ChunkSize
	plain, err := f.chunk(context.Background(), i)
	if err != nil {
		return 0, err
	}
	n := copy(p, plain[f.pos-i*replica.ChunkSize:])
	f.pos += int64(n)
	return n, nil
}

// chunk returns decrypted chunk i, fetching it (and the next few) from the
// node when it is not cached.
func (f *remoteFile) chunk(ctx context.Context, i int64) ([]byte, error) {
	cache := f.s.remote().cache
	if b, ok := cache.get(f.cacheID + strconv.FormatInt(i, 10)); ok {
		return b, nil
	}
	h := f.head.h
	last := min(i+relayWindow, h.Chunks()) - 1
	from, _, _ := h.ChunkSpan(i)
	lo, ln, _ := h.ChunkSpan(last)
	data, err := f.s.remoteRange(ctx, &f.rb, from, lo+ln-1)
	if err != nil {
		return nil, err
	}
	var want []byte
	for k := i; k <= last; k++ {
		off, n, _ := h.ChunkSpan(k)
		off -= from
		if off+n > int64(len(data)) {
			return nil, errors.New("the storage node returned less data than asked for")
		}
		plain, err := h.OpenChunk(f.head.key, replica.BlobID(f.rb.UserID, f.rb.SHA256), k, data[off:off+n])
		if err != nil {
			slog.Error("remote file failed its integrity check", "user", f.rb.UserID, "sha", f.rb.SHA256, "node", f.rb.NodeID, "chunk", k)
			return nil, fmt.Errorf("the copy on the storage node is damaged: %w", err)
		}
		cache.put(f.cacheID+strconv.FormatInt(k, 10), plain)
		if k == i {
			want = plain
		}
	}
	return want, nil
}

// remoteHeader reads, checks and caches a remote file's header and key.
func (s *Server) remoteHeader(ctx context.Context, rb *store.RemoteBlob) (*remoteHead, error) {
	rs := s.remote()
	k := fmt.Sprintf("%d/%s", rb.UserID, rb.SHA256)
	rs.mu.Lock()
	if h, ok := rs.heads[k]; ok {
		rs.mu.Unlock()
		return h, nil
	}
	rs.mu.Unlock()
	end := int64(replica.MaxHeaderV2) - 1
	if rb.CipherSize > 0 {
		end = min(end, rb.CipherSize-1)
	}
	b, err := s.remoteRange(ctx, rb, 0, end)
	if err != nil {
		return nil, err
	}
	h, err := replica.ParseV2Header(b)
	if err != nil || h.KeyID != rb.KeyID || h.Size != rb.PlainSize {
		return nil, errors.New("the copy on the storage node is damaged (its header does not match)")
	}
	ok, err := s.nodeOriginKey(ctx, rb.NodeID)
	if err != nil {
		return nil, err
	}
	if ok.id != rb.KeyID {
		return nil, errors.New("this file was stored with a key this server no longer has")
	}
	fk, err := h.FileKey(ok.priv)
	if err != nil {
		return nil, errors.New("the copy on the storage node cannot be decrypted")
	}
	head := &remoteHead{h: h, key: fk}
	rs.mu.Lock()
	if len(rs.heads) > 4096 {
		rs.heads = map[string]*remoteHead{}
	}
	rs.heads[k] = head
	rs.mu.Unlock()
	return head, nil
}

// errNodeOffline: a node holding a file cannot be reached.
var errNodeOffline = errors.New("the storage node holding this file is offline right now")

// remoteRange fetches encrypted bytes [from, to] of a remote file.
func (s *Server) remoteRange(ctx context.Context, rb *store.RemoteBlob, from, to int64) ([]byte, error) {
	n, err := s.st.StorageNode(ctx, rb.NodeID)
	if err != nil {
		return nil, errNodeOffline
	}
	key, err := s.st.StorageNodeReadKey(ctx, rb.NodeID)
	if err != nil || !strings.HasPrefix(n.PublicURL, "https://") {
		return nil, errNodeOffline
	}
	path := nodeapi.RemotePath(rb.LocationID, rb.UserID, rb.SHA256)
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(n.PublicURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	rng := fmt.Sprintf("bytes=%d-%d", from, to)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Range", rng)
	req.Header.Set(nodeapi.HeaderTime, ts)
	req.Header.Set(nodeapi.HeaderSig, nodeapi.SignRead(key, "GET", path, rng, ts))
	resp, err := s.remote().http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errNodeOffline
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent, http.StatusOK:
	case http.StatusNotFound:
		_ = s.st.SetRemoteBlobState(context.WithoutCancel(ctx), rb.UserID, rb.SHA256, "missing", "the storage node no longer has this file")
		return nil, errors.New("the storage node no longer has this file")
	default:
		return nil, fmt.Errorf("the storage node answered HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, to-from+1))
	if err != nil {
		return nil, errNodeOffline
	}
	if resp.StatusCode == http.StatusOK && from > 0 {
		return nil, errors.New("the storage node does not support range reads")
	}
	return b, nil
}

// Media gives FFmpeg a loopback URL for the file.
func (f *remoteFile) Media() (media.Input, func()) {
	return f.s.loopbackURL(f.rb.UserID, f.rb.SHA256)
}

// --- loopback relay for FFmpeg ---

func (s *Server) loopbackURL(userID int64, sha string) (media.Input, func()) {
	rs := s.remote()
	rs.lnOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			rs.lnError = err
			return
		}
		rs.lnAddr = ln.Addr().String()
		srv := &http.Server{Handler: http.HandlerFunc(s.serveLoopback), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
	})
	if rs.lnError != nil {
		return media.URL("unavailable:"), func() {}
	}
	tok := auth.RandomToken(24)
	rs.mu.Lock()
	rs.tokens[tok] = relayToken{userID: userID, sha: sha, expires: time.Now().Add(12 * time.Hour)}
	rs.mu.Unlock()
	return media.URL("http://" + rs.lnAddr + "/r/" + tok), func() {
		rs.mu.Lock()
		delete(rs.tokens, tok)
		rs.mu.Unlock()
	}
}

func (s *Server) serveLoopback(w http.ResponseWriter, r *http.Request) {
	rs := s.remote()
	tok := strings.TrimPrefix(r.URL.Path, "/r/")
	rs.mu.Lock()
	t, ok := rs.tokens[tok]
	rs.mu.Unlock()
	if !ok || time.Now().After(t.expires) {
		http.NotFound(w, r)
		return
	}
	f, err := s.openBlob(r.Context(), t.userID, t.sha)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer f.Close()
	if err := precheck(r, f); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// --- chunk cache ---

type chunkCache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	order *list.List
	items map[string]*list.Element
}

type chunkEntry struct {
	key  string
	data []byte
}

func newChunkCache(max int64) *chunkCache {
	return &chunkCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func (c *chunkCache) get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*chunkEntry).data, true
	}
	return nil, false
}

func (c *chunkCache) put(k string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[k]; ok {
		return
	}
	c.items[k] = c.order.PushFront(&chunkEntry{k, b})
	c.size += int64(len(b))
	for c.size > c.max && c.order.Len() > 0 {
		e := c.order.Back()
		ce := e.Value.(*chunkEntry)
		c.order.Remove(e)
		delete(c.items, ce.key)
		c.size -= int64(len(ce.data))
	}
}

// --- moving files to a node ---

// startMove moves a user's files to their node in the background (one
// move per user at a time). It reports false when one is running.
func (rs *remoteService) startMove(s *Server, userID, nodeID int64) bool {
	rs.mu.Lock()
	if m, ok := rs.moves[userID]; ok && m.Running {
		rs.mu.Unlock()
		return false
	}
	st := &moveState{NodeID: nodeID, Running: true, Started: time.Now().UnixMilli()}
	rs.moves[userID] = st
	rs.mu.Unlock()
	s.ops.wg.Add(1)
	go func() {
		defer s.ops.wg.Done()
		err := s.runMove(context.Background(), userID, nodeID, st)
		rs.mu.Lock()
		st.Running, st.Ended = false, time.Now().UnixMilli()
		if err != nil {
			st.Error = err.Error()
		}
		rs.mu.Unlock()
		slog.Info("move to storage node finished", "user", userID, "node", nodeID, "moved", st.Moved, "failed", st.Failed, "err", err)
	}()
	return true
}

func (s *Server) runMove(ctx context.Context, userID, nodeID int64, st *moveState) error {
	rs := s.remote()
	tried := map[string]bool{}
	for {
		n, err := s.st.StorageNode(ctx, nodeID)
		if err != nil {
			return err
		}
		if ok, why := s.remoteReady(ctx, n); !ok {
			return errors.New(why)
		}
		loc, err := s.nodeStorageLocation(ctx, nodeID)
		if err != nil {
			return err
		}
		refs, sizes, err := s.st.BlobsToMove(ctx, userID, moveAttempts, 50)
		if err != nil {
			return err
		}
		progressed := false
		for i, ref := range refs {
			if tried[ref.SHA256] {
				continue
			}
			tried[ref.SHA256] = true
			progressed = true
			err := s.moveBlob(ctx, userID, ref, sizes[i], nodeID, loc.ID)
			rs.mu.Lock()
			if err != nil {
				st.Failed++
				st.Error = err.Error()
			} else {
				st.Moved++
				st.Bytes += sizes[i]
			}
			rs.mu.Unlock()
		}
		if !progressed {
			return nil
		}
	}
}

// moveBlob copies one blob to the node encrypted for its key pair, checks
// what the node stored and then removes the local file.
func (s *Server) moveBlob(ctx context.Context, userID int64, ref store.BlobRef, size, nodeID, locID int64) error {
	ok, err := s.nodeOriginKey(ctx, nodeID)
	if err != nil {
		return err
	}
	if err := s.st.StartRemoteBlob(ctx, store.RemoteBlob{UserID: userID, SHA256: ref.SHA256, NodeID: nodeID, LocationID: locID, KeyID: ok.id, PlainSize: size}); err != nil {
		return err
	}
	id, err := s.enqueueJob(ctx, nodeID, nodeapi.JobPut, jobMeta{
		JobPayload: nodeapi.JobPayload{LocationID: locID, UserID: userID, SHA256: ref.SHA256, Size: size, Kind: nodeapi.KindRemote},
		Target:     fmt.Sprintf("remote:%d", nodeID), Purpose: nodeapi.KindRemote, PrimaryLoc: ref.LocationID,
	})
	if err != nil {
		_ = s.st.FailRemoteBlob(ctx, userID, ref.SHA256, err.Error())
		return err
	}
	// Large files over a home connection: allow about 1 MB/s plus margin.
	timeout := 10*time.Minute + time.Duration(size/(1<<20))*time.Second
	if err := s.waitJob(ctx, id, timeout); err != nil {
		_ = s.st.FailRemoteBlob(ctx, userID, ref.SHA256, err.Error())
		return err
	}
	return nil
}

// remoteMoved applies a finished move job: on a verified store the blob
// becomes remote and its local file is removed.
func (s *Server) remoteMoved(ctx context.Context, meta jobMeta, res nodeapi.JobResult, enc, plain string) error {
	switch {
	case !res.OK:
		return fmt.Errorf("the node could not store the file: %s", res.Error)
	case plain != meta.SHA256:
		_ = s.st.RecordPrimaryCheck(ctx, meta.UserID, meta.SHA256, false, "checksum mismatch while moving to a node")
		return errors.New("the local file is damaged (its checksum does not match); it was not moved")
	case enc == "" || res.SHA256 != enc:
		return errors.New("the node stored different data than was sent")
	}
	if err := s.st.FinishRemoteBlob(ctx, meta.UserID, meta.SHA256, enc, res.Size); err != nil {
		return err
	}
	if err := s.blobs.RemovePrimary(meta.PrimaryLoc, meta.UserID, meta.SHA256); err != nil {
		slog.Warn("moved file kept locally (could not remove it)", "user", meta.UserID, "sha", meta.SHA256, "err", err)
	}
	return nil
}

// contentError is the API error for content that cannot be opened.
func contentError(err error) error {
	if errors.Is(err, errNodeOffline) {
		return errf(503, "node_offline", "This file is kept on a storage node that is offline right now. Try again later.")
	}
	if errors.Is(err, store.ErrNotFound) {
		return err
	}
	return errf(503, "content_unavailable", "File content is unavailable on the storage device")
}

// precheck reads the first chunk a request needs from a remote file before
// any response header is sent, so an offline node or a damaged copy is an
// error status rather than a cut-off download.
func precheck(r *http.Request, f storedFile) error {
	rf, ok := f.(*remoteFile)
	if !ok {
		return nil
	}
	var start int64
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		first, _, _ := strings.Cut(strings.TrimPrefix(rng, "bytes="), ",")
		lo, hi, _ := strings.Cut(first, "-")
		switch {
		case lo != "":
			start, _ = strconv.ParseInt(lo, 10, 64)
		case hi != "":
			n, _ := strconv.ParseInt(hi, 10, 64)
			start = rf.head.h.Size - n
		}
	}
	if start < 0 || start >= rf.head.h.Size {
		return nil
	}
	_, err := rf.chunk(r.Context(), start/replica.ChunkSize)
	return err
}

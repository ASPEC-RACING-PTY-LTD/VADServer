package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/mimetypes"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pathutil"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

type uploadReq struct {
	Path       string   `json:"path"`
	ParentID   int64    `json:"parent_id"`
	Name       string   `json:"name"`
	Size       int64    `json:"size"`
	SHA256     string   `json:"sha256"`
	ModifiedAt int64    `json:"modified_at"`
	Tags       []string `json:"tags"`
	OnConflict string   `json:"on_conflict"`
}

type uploadView struct {
	ID            string  `json:"id"`
	Path          string  `json:"path"`
	Size          int64   `json:"size"`
	ChunkSize     int64   `json:"chunk_size"`
	ChunksTotal   int64   `json:"chunks_total"`
	Received      []int64 `json:"received"`
	BytesReceived int64   `json:"bytes_received"`
	Status        string  `json:"status"`
	ExpiresAt     int64   `json:"expires_at"`
}

func viewUpload(u *store.Upload) uploadView {
	return uploadView{ID: u.ID, Path: u.Path, Size: u.Size, ChunkSize: u.ChunkSize, ChunksTotal: u.ChunksTotal,
		Received: u.ReceivedChunks(), BytesReceived: u.BytesReceived, Status: u.Status, ExpiresAt: u.ExpiresAt}
}

// target resolves the destination path of an upload request.
func (s *Server) uploadTarget(r *http.Request, u *store.User, req *uploadReq) ([]string, string, error) {
	if req.Path != "" {
		segs, err := pathutil.Split(req.Path)
		if err != nil {
			return nil, "", errf(400, "invalid_path", "%s", err.Error())
		}
		if len(segs) == 0 {
			return nil, "", errf(400, "invalid_path", "Path must include a file name")
		}
		return segs[:len(segs)-1], segs[len(segs)-1], nil
	}
	name := strings.TrimSpace(req.Name)
	if err := pathutil.ValidateName(name); err != nil {
		return nil, "", errf(400, "invalid_name", "%s", err.Error())
	}
	parent := req.ParentID
	if parent == 0 {
		parent = u.RootID
	}
	pn, err := s.st.Node(r.Context(), u.ID, parent)
	if err != nil {
		return nil, "", store.ErrParentGone
	}
	if !pn.IsDir {
		return nil, "", store.ErrNotDir
	}
	p, err := s.st.NodePath(r.Context(), u.ID, parent)
	if err != nil {
		return nil, "", err
	}
	segs, _ := pathutil.Split(p)
	return segs, name, nil
}

func (s *Server) userLocation(r *http.Request, u *store.User) (int64, error) {
	if u.LocationID != nil {
		if l, ok := s.blobs.Location(*u.LocationID); ok && l.Enabled {
			return l.ID, nil
		}
	}
	l, err := s.st.DefaultLocation(r.Context())
	if err != nil {
		return 0, err
	}
	return l.ID, nil
}

func (s *Server) handleUploadCreate(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	u := p.User
	var req uploadReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.SHA256 = strings.ToLower(strings.TrimSpace(req.SHA256))
	if req.SHA256 != "" && !storage.ValidSHA256(req.SHA256) {
		writeErr(w, r, errf(400, "invalid_hash", "sha256 must be 64 lower-case hex characters"))
		return
	}
	if p.Device != nil && req.SHA256 == "" {
		writeErr(w, r, errf(400, "hash_required", "Device uploads must declare the file's sha256"))
		return
	}
	if req.Size < 0 || req.Size > 1<<46 {
		writeErr(w, r, errf(400, "invalid_size", "Invalid file size"))
		return
	}
	switch req.OnConflict {
	case "":
		req.OnConflict = "replace"
	case "replace", "rename", "skip":
	default:
		writeErr(w, r, errf(400, "invalid_conflict", "on_conflict must be replace, rename or skip"))
		return
	}
	tags, err := cleanTags(req.Tags)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req.Tags = tags
	settings := s.settings.Get()
	if settings.MaxFileBytes > 0 && req.Size > settings.MaxFileBytes {
		writeErr(w, r, errf(413, "file_too_large", "Files larger than %d bytes are not allowed on this server", settings.MaxFileBytes))
		return
	}
	dirSegs, name, err := s.uploadTarget(r, u, &req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	fullPath := "/" + strings.Join(append(append([]string{}, dirSegs...), name), "/")
	if req.ModifiedAt <= 0 {
		req.ModifiedAt = time.Now().UnixMilli()
	}

	// Deduplication: the user already stores identical content.
	if req.SHA256 != "" {
		var result *store.CommitResult
		linked, err := s.blobs.LinkExisting(r.Context(), u.ID, req.SHA256, func(loc int64) error {
			var cerr error
			result, cerr = s.st.CommitFile(r.Context(), store.CommitRequest{
				User: u, DirSegs: dirSegs, Name: name, Size: req.Size, SHA256: req.SHA256, Mime: mimetypes.ByName(name),
				ModifiedAt: req.ModifiedAt, Tags: req.Tags, OnConflict: req.OnConflict, DeviceID: p.deviceID(), LocationID: loc,
				KeepReplaced: settings.KeepReplacedVersions,
			})
			return cerr
		})
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if linked {
			s.blobs.Remove(r.Context(), result.Freed)
			status := "deduplicated"
			if result.Result == "unchanged" || result.Result == "skipped" {
				status = result.Result
			}
			_ = s.st.RecordTransfer(r.Context(), store.Transfer{UserID: u.ID, DeviceID: p.deviceID(), Path: fullPath, Size: req.Size, BytesDone: 0, Status: status, Source: sourceName(p)})
			if result.Result != "unchanged" && result.Result != "skipped" {
				s.activity(r, u.ID, "file.uploaded", result.Node.Path, "deduplicated (content already stored)", false)
			}
			writeJSON(w, 200, map[string]any{"status": "complete", "result": status, "node": result.Node})
			return
		}
	}

	// Resume an existing session for identical content and target.
	// A session whose temp data is missing or short (lost in a move or a
	// crash) is discarded and a fresh one started, so the client re-sends
	// the whole file.
	if existing, err := s.st.FindResumableUpload(r.Context(), u.ID, p.deviceID(), fullPath, req.Size, req.SHA256); err == nil {
		if s.uploadDataIntact(existing) {
			writeJSON(w, 200, map[string]any{"status": "open", "resumed": true, "upload": viewUpload(existing)})
			return
		}
		s.discardUpload(r.Context(), existing)
	}

	if u.QuotaBytes > 0 {
		pending, err := s.st.PendingUserBytes(r.Context(), u.ID, "")
		if err != nil {
			writeErr(w, r, err)
			return
		}
		fresh, _ := s.st.UserByID(r.Context(), u.ID)
		if fresh.UsedBytes+pending+req.Size > fresh.QuotaBytes {
			writeErr(w, r, &apiError{Status: 413, Code: "quota_exceeded", Message: "Storage quota exceeded",
				Details: map[string]any{"quota": fresh.QuotaBytes, "used": fresh.UsedBytes, "pending": pending, "requested": req.Size}})
			return
		}
	}
	locID, err := s.userLocation(r, u)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.blobs.CheckSpace(r.Context(), locID, req.Size); err != nil {
		writeErr(w, r, err)
		return
	}
	chunk := s.cfg.ChunkSize
	total := (req.Size + chunk - 1) / chunk
	now := time.Now()
	up := &store.Upload{
		ID: auth.RandomToken(18), UserID: u.ID, DeviceID: p.deviceID(), Path: fullPath, Size: req.Size, SHA256: req.SHA256,
		ChunkSize: chunk, ChunksTotal: total, LocationID: locID, ModifiedAt: req.ModifiedAt, Tags: req.Tags, OnConflict: req.OnConflict,
		Status: "open", CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), ExpiresAt: now.Add(s.cfg.UploadSessionTTL).UnixMilli(),
	}
	tmp, err := s.blobs.TempPath(locID, up.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		writeErr(w, r, fmt.Errorf("create temp file: %w", err))
		return
	}
	f.Close()
	if err := s.st.CreateUpload(r.Context(), up); err != nil {
		os.Remove(tmp)
		writeErr(w, r, err)
		return
	}
	up.Status = "open"
	writeJSON(w, 201, map[string]any{"status": "open", "upload": viewUpload(up)})
}

func sourceName(p *Principal) string {
	if p.Device != nil {
		return "device"
	}
	return "web"
}

func (s *Server) loadUpload(w http.ResponseWriter, r *http.Request) *store.Upload {
	u := principal(r).User
	id := chi.URLParam(r, "id")
	if !storage.ValidUploadID(id) {
		writeErr(w, r, errf(404, "not_found", "Upload not found"))
		return nil
	}
	up, err := s.st.Upload(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "Upload not found or expired; start a new upload"))
		return nil
	}
	return up
}

func (s *Server) handleUploadGet(w http.ResponseWriter, r *http.Request) {
	up := s.loadUpload(w, r)
	if up == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"upload": viewUpload(up)})
}

func (s *Server) handleUploadList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListUploads(r.Context(), principal(r).User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views := make([]uploadView, 0, len(list))
	for i := range list {
		views = append(views, viewUpload(&list[i]))
	}
	writeJSON(w, 200, map[string]any{"uploads": views})
}

func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	up := s.loadUpload(w, r)
	if up == nil {
		return
	}
	if up.Status != "open" {
		writeErr(w, r, errf(409, "upload_closed", "Upload is %s", up.Status))
		return
	}
	idx, err := strconv.ParseInt(chi.URLParam(r, "index"), 10, 64)
	if err != nil || idx < 0 || idx >= up.ChunksTotal {
		writeErr(w, r, errf(400, "bad_chunk", "Chunk index out of range"))
		return
	}
	want := up.ChunkLen(idx)
	zipped := strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "zstd")
	switch enc := r.Header.Get("Content-Encoding"); {
	case zipped:
		// upload.zstd: the body is a zstd frame of the chunk. Bound the
		// compressed size; the decoded size must still be exactly want.
		if r.ContentLength <= 0 || r.ContentLength > want+want/8+(64<<10) {
			writeErr(w, r, errf(400, "bad_length", "Compressed chunk %d has an invalid length (%d)", idx, r.ContentLength))
			return
		}
	case enc != "" && !strings.EqualFold(enc, "identity"):
		writeErr(w, r, errf(415, "unsupported_encoding", "Chunk encoding %q is not supported", enc))
		return
	case r.ContentLength != want:
		writeErr(w, r, errf(400, "bad_length", "Chunk %d must be exactly %d bytes (got %d)", idx, want, r.ContentLength))
		return
	}
	declared := strings.ToLower(r.Header.Get("X-Chunk-SHA256"))
	bodySHA := "" // hash of the bytes on the wire, checked for compressed chunks
	if p.Device != nil {
		// The signed body hash is authoritative for devices. For a
		// compressed chunk it covers the compressed bytes; the raw chunk is
		// then checked against X-Chunk-SHA256.
		if zipped {
			bodySHA = p.DeclaredBodySHA
		} else {
			declared = p.DeclaredBodySHA
		}
	}
	if (declared != "" && !storage.ValidSHA256(declared)) || (zipped && !storage.ValidSHA256(declared)) {
		writeErr(w, r, errf(400, "invalid_hash", "Invalid chunk hash"))
		return
	}

	// Server capacity: wait briefly for a write slot, then tell the client
	// to back off. The desktop client's adaptive concurrency reacts to 503.
	select {
	case s.chunkSem <- struct{}{}:
		defer func() { <-s.chunkSem }()
	case <-time.After(3 * time.Second):
		w.Header().Set("Retry-After", "2")
		w.Header().Set("X-VD-Capacity", "saturated")
		writeErr(w, r, errf(503, "server_busy", "Server is busy, retry shortly"))
		return
	case <-r.Context().Done():
		return
	}

	// Hold the upload lock shared while writing: complete and cancel take it
	// exclusively, so no chunk is ever written into a file that is being
	// hashed, committed or deleted. The chunk lock serialises writers of the
	// same index (duplicate retries).
	unlockUpload, err := uploadLocks.acquire(r.Context(), up.ID, false)
	if err != nil {
		return
	}
	defer unlockUpload()
	unlockChunk, err := uploadLocks.acquire(r.Context(), up.ID+"#"+strconv.FormatInt(idx, 10), true)
	if err != nil {
		return
	}
	defer unlockChunk()
	// Re-read under the locks: the session may have been completed or
	// cancelled since loadUpload, and another writer may have stored this chunk.
	cur, err := s.st.Upload(r.Context(), up.UserID, up.ID)
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "Upload not found or expired; start a new upload"))
		return
	}
	if cur.Status != "open" {
		writeErr(w, r, errf(409, "upload_closed", "Upload is %s", cur.Status))
		return
	}
	already := cur.Received[idx/8]&(1<<(idx%8)) != 0

	tmp, err := s.blobs.TempPath(up.LocationID, up.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY, 0)
	if err != nil {
		unlockChunk()
		unlockUpload()
		s.discardUpload(r.Context(), cur)
		writeErr(w, r, errf(410, "upload_gone", "Upload data is no longer available; start a new upload"))
		return
	}
	defer f.Close()
	ow := io.NewOffsetWriter(f, idx*up.ChunkSize)
	var body io.Reader = r.Body
	var wire hashReader
	if zipped {
		wire = hashReader{r: io.LimitReader(r.Body, r.ContentLength), h: sha256.New()}
		zr, err := compress.NewFrameReader(&wire)
		if err != nil {
			writeErr(w, r, errf(400, "bad_encoding", "Invalid compressed chunk"))
			return
		}
		defer zr.Close()
		body = zr
	}
	var got string
	if already {
		// A retry of a stored chunk is staged and verified first, so a
		// corrupted retry can never overwrite good data.
		got, err = receiveChunkStaged(tmp, idx, ow, body, want, declared)
	} else {
		// The region is not yet marked received, so writing in place is safe
		// even if verification then fails.
		got, err = receiveChunk(ow, body, idx, want, declared)
	}
	if err == nil && zipped && bodySHA != "" {
		// The rest of the frame (end marks) is part of the signed bytes.
		_, _ = io.Copy(io.Discard, &wire)
		if hex.EncodeToString(wire.h.Sum(nil)) != bodySHA {
			err = errf(422, "chunk_hash_mismatch", "Chunk %d was corrupted in transit; retry", idx)
		}
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := f.Sync(); err != nil {
		writeErr(w, r, fmt.Errorf("sync chunk: %w", err))
		return
	}
	if _, err := s.st.MarkChunk(r.Context(), up.UserID, up.ID, idx, want, time.Now().Add(s.cfg.UploadSessionTTL).UnixMilli()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			err = errf(409, "upload_closed", "Upload is no longer open")
		}
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"index": idx, "sha256": got})
}

// receiveChunk copies exactly want bytes of body to dst while hashing them
// and checks the result against the declared hash (if any). It returns the
// chunk's sha256.
func receiveChunk(dst io.Writer, body io.Reader, idx, want int64, declared string) (string, error) {
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(dst, h), io.LimitReader(body, want+1), make([]byte, 256<<10))
	if err != nil {
		return "", errf(400, "read_failed", "Failed to receive chunk: %v", err)
	}
	if n != want {
		return "", errf(400, "bad_length", "Chunk %d must be exactly %d bytes", idx, want)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if declared != "" && got != declared {
		return "", errf(422, "chunk_hash_mismatch", "Chunk %d was corrupted in transit; retry", idx)
	}
	return got, nil
}

// receiveChunkStaged receives a chunk into a staging file next to the
// upload's temp file and copies it to dst only after it was verified. The
// staging file does not match an upload id, so CleanOrphans removes it if
// the server crashes mid-copy.
func receiveChunkStaged(tmp string, idx int64, dst io.Writer, body io.Reader, want int64, declared string) (string, error) {
	stagePath := tmp + "." + strconv.FormatInt(idx, 10) + ".stage"
	sf, err := os.OpenFile(stagePath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o640)
	if err != nil {
		return "", fmt.Errorf("create chunk staging file: %w", err)
	}
	defer func() {
		sf.Close()
		_ = os.Remove(stagePath)
	}()
	got, err := receiveChunk(sf, body, idx, want, declared)
	if err != nil {
		return "", err
	}
	if _, err := sf.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind chunk staging file: %w", err)
	}
	if n, err := io.CopyBuffer(dst, io.LimitReader(sf, want), make([]byte, 256<<10)); err != nil || n != want {
		if err == nil {
			err = io.ErrShortWrite
		}
		return "", fmt.Errorf("copy staged chunk: %w", err)
	}
	return got, nil
}

// uploadLocks guards each upload's temp file: chunk writers hold the upload
// key shared; status transitions (complete, cancel) hold it exclusively.
var uploadLocks = &keyedLocks{m: map[string]*keyedLock{}}

type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	sync.RWMutex
	refs int
}

// acquire locks key (exclusively or shared) and returns the release
// function. It gives up when ctx ends; entries are freed when unused.
func (k *keyedLocks) acquire(ctx context.Context, key string, exclusive bool) (func(), error) {
	k.mu.Lock()
	l := k.m[key]
	if l == nil {
		l = &keyedLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	lock, tryLock, unlock := l.RLock, l.TryRLock, l.RUnlock
	if exclusive {
		lock, tryLock, unlock = l.Lock, l.TryLock, l.Unlock
	}
	var once sync.Once
	release := func() { // safe to call more than once
		once.Do(func() {
			unlock()
			k.mu.Lock()
			if l.refs--; l.refs == 0 {
				delete(k.m, key)
			}
			k.mu.Unlock()
		})
	}
	if tryLock() {
		return release, nil
	}
	got := make(chan struct{})
	go func() {
		lock()
		close(got)
	}()
	select {
	case <-got:
		return release, nil
	case <-ctx.Done():
		go func() {
			<-got
			release()
		}()
		return nil, ctx.Err()
	}
}

func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	up := s.loadUpload(w, r)
	if up == nil {
		return
	}
	// Take the upload lock exclusively so in-flight chunk writes finish
	// before the status leaves "open"; later chunk writes see the new status.
	unlock, err := uploadLocks.acquire(r.Context(), up.ID, true)
	if err != nil {
		return
	}
	if cur, err := s.st.Upload(r.Context(), up.UserID, up.ID); err == nil {
		up = cur
	}
	if !up.Complete() {
		unlock()
		writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "incomplete", "message": "Not all chunks have been received"}, "upload": viewUpload(up)})
		return
	}
	if !s.uploadDataIntact(up) {
		unlock()
		s.discardUpload(r.Context(), up)
		writeErr(w, r, errf(410, "upload_gone", "Upload data is missing; start a new upload"))
		return
	}
	ok, err := s.st.SetUploadStatus(r.Context(), up.ID, "open", "finalizing")
	unlock()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, r, errf(409, "upload_busy", "Upload is already being finalised (status %s)", up.Status))
		return
	}
	res, err := s.finalize(r.Context(), p, up)
	if err != nil {
		var ae *apiError
		permanent := errors.As(err, &ae) || errors.Is(err, store.ErrQuota) || errors.Is(err, store.ErrIsDir) || errors.Is(err, store.ErrNotDir)
		if permanent {
			_, _ = s.st.SetUploadStatus(context.WithoutCancel(r.Context()), up.ID, "finalizing", "failed")
			_ = s.st.FinishTransfer(context.WithoutCancel(r.Context()), nil, up.TransferID, "failed", errMessage(err), -1)
			if tmp, terr := s.blobs.TempPath(up.LocationID, up.ID); terr == nil {
				_ = os.Remove(tmp)
			}
		} else {
			_, _ = s.st.SetUploadStatus(context.WithoutCancel(r.Context()), up.ID, "finalizing", "open")
		}
		writeErr(w, r, err)
		return
	}
	s.blobs.Remove(r.Context(), res.Freed)
	if res.Result != "unchanged" && res.Result != "skipped" {
		s.activity(r, up.UserID, "file.uploaded", res.Node.Path, humanBytes(up.Size)+" "+res.Result, false)
	}
	writeJSON(w, 200, map[string]any{"status": "complete", "result": res.Result, "node": res.Node})
}

func errMessage(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// finalize verifies the assembled file and commits it atomically.
func (s *Server) finalize(ctx context.Context, p *Principal, up *store.Upload) (*store.CommitResult, error) {
	tmp, err := s.blobs.TempPath(up.LocationID, up.ID)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return nil, errf(410, "upload_gone", "Upload data is missing; start a new upload")
	}
	if info.Size() != up.Size {
		return nil, errf(422, "size_mismatch", "Assembled file is %d bytes, expected %d", info.Size(), up.Size)
	}
	sum, err := storage.HashFile(tmp)
	if err != nil {
		return nil, err
	}
	if up.SHA256 != "" && sum != up.SHA256 {
		slog.Warn("upload hash mismatch", "upload", up.ID, "declared", up.SHA256, "actual", sum)
		return nil, errf(422, "hash_mismatch", "File content does not match the declared hash; the file may have changed during upload")
	}
	segs, err := pathutil.Split(up.Path)
	if err != nil || len(segs) == 0 {
		return nil, errf(400, "invalid_path", "Invalid upload path")
	}
	name := segs[len(segs)-1]
	mt := mimetypes.ByName(name)
	if mt == "application/octet-stream" {
		if f, err := os.Open(tmp); err == nil {
			head := make([]byte, 512)
			n, _ := io.ReadFull(f, head)
			f.Close()
			mt = http.DetectContentType(head[:n])
			if i := strings.IndexByte(mt, ';'); i >= 0 {
				mt = mt[:i]
			}
			if mt == "text/html" || mt == "text/xml" {
				mt = "application/octet-stream"
			}
		}
	}
	user, err := s.st.UserByID(ctx, up.UserID)
	if err != nil {
		return nil, err
	}
	var res *store.CommitResult
	err = s.blobs.Place(ctx, up.UserID, sum, tmp, up.LocationID, func(loc int64) error {
		var cerr error
		res, cerr = s.st.CommitFile(ctx, store.CommitRequest{
			User: user, DirSegs: segs[:len(segs)-1], Name: name, Size: up.Size, SHA256: sum, Mime: mt, ModifiedAt: up.ModifiedAt,
			Tags: up.Tags, OnConflict: up.OnConflict, DeviceID: up.DeviceID, LocationID: loc, TransferID: up.TransferID, UploadID: up.ID,
			KeepReplaced: s.settings.Get().KeepReplacedVersions,
		})
		return cerr
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Server) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	up := s.loadUpload(w, r)
	if up == nil {
		return
	}
	if !s.cancelUpload(r.Context(), up, "cancelled by user") {
		if cur, err := s.st.Upload(r.Context(), up.UserID, up.ID); err == nil && cur.Status == "finalizing" {
			writeErr(w, r, errf(409, "upload_busy", "Upload is being finalised and cannot be cancelled"))
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// cancelUpload deletes an upload session and its temp file. Uploads being
// finalised are left alone (deleting the temp file would break the commit);
// it reports whether the session was cancelled.
func (s *Server) cancelUpload(ctx context.Context, up *store.Upload, reason string) bool {
	unlock, err := uploadLocks.acquire(ctx, up.ID, true)
	if err != nil {
		return false
	}
	defer unlock()
	deleted, err := s.st.DeleteUploadUnlessFinalizing(ctx, up.ID)
	if err != nil || !deleted {
		return false
	}
	if tmp, err := s.blobs.TempPath(up.LocationID, up.ID); err == nil {
		_ = os.Remove(tmp)
	}
	if up.Status == "open" || up.Status == "finalizing" {
		_ = s.st.FinishTransfer(ctx, nil, up.TransferID, "cancelled", reason, -1)
	}
	return true
}

// uploadDataIntact reports whether an open session's temp file still holds
// every chunk the session has recorded as received.
func (s *Server) uploadDataIntact(up *store.Upload) bool {
	tmp, err := s.blobs.TempPath(up.LocationID, up.ID)
	if err != nil {
		return false
	}
	info, err := os.Stat(tmp)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	var end int64
	for i := up.ChunksTotal - 1; i >= 0; i-- {
		if up.Received[i/8]&(1<<(i%8)) != 0 {
			end = i*up.ChunkSize + up.ChunkLen(i)
			break
		}
	}
	return info.Size() >= end
}

// discardUpload drops a session whose data was lost: the session, its temp
// file and its transfer record go, so the next attempt registers as a new
// transfer and the client uploads the file again in full.
func (s *Server) discardUpload(ctx context.Context, up *store.Upload) {
	ctx = context.WithoutCancel(ctx)
	unlock, err := uploadLocks.acquire(ctx, up.ID, true)
	if err != nil {
		return
	}
	defer unlock()
	if deleted, err := s.st.DeleteUploadUnlessFinalizing(ctx, up.ID); err != nil || !deleted {
		return
	}
	if tmp, err := s.blobs.TempPath(up.LocationID, up.ID); err == nil {
		_ = os.Remove(tmp)
	}
	if up.TransferID > 0 {
		_ = s.st.DeleteTransfer(ctx, up.TransferID)
	}
	slog.Info("upload data missing; session discarded", "upload", up.ID, "path", up.Path)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// hashReader hashes the bytes read through it (the compressed wire body).
type hashReader struct {
	r io.Reader
	h hash.Hash
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	return n, err
}

func init() { implementFeature(wire.FeatureUploadZstd) }

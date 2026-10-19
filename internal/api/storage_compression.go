package api

// At-rest storage compression (admin): a background job compresses stored
// files with zstd where that saves space (internal/storage/compress.go). It
// runs every maintenance cycle while the storage_compression setting is on,
// and on demand from the admin console.

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

type compressionState struct {
	mu      sync.Mutex
	running bool
	current storage.CompressStats
	last    *storage.CompressStats
	lastErr string
}

var compressionStates sync.Map // *Server -> *compressionState

func (s *Server) compression() *compressionState {
	v, _ := compressionStates.LoadOrStore(s, &compressionState{})
	return v.(*compressionState)
}

// RunStorageCompression runs one compression pass unless one is running. It
// returns false when a pass was already running.
func (s *Server) RunStorageCompression(ctx context.Context) bool {
	cs := s.compression()
	cs.mu.Lock()
	if cs.running {
		cs.mu.Unlock()
		return false
	}
	cs.running = true
	cs.current = storage.CompressStats{StartedAt: time.Now().UnixMilli()}
	cs.mu.Unlock()
	st, err := s.blobs.CompressStored(ctx, func(p storage.CompressStats) {
		cs.mu.Lock()
		cs.current = p
		cs.mu.Unlock()
	})
	cs.mu.Lock()
	cs.running = false
	cs.last = &st
	cs.lastErr = ""
	if err != nil {
		cs.lastErr = err.Error()
	}
	cs.mu.Unlock()
	if st.Compressed > 0 || err != nil {
		slog.Info("storage compression pass", "compressed", st.Compressed, "saved_bytes", st.BytesBefore-st.BytesAfter, "failed", st.Failed, "err", err)
	}
	return true
}

// MaybeCompressStorage runs a pass when the setting is on (maintenance).
func (s *Server) MaybeCompressStorage(ctx context.Context) {
	if s.settings.Get().StorageCompression {
		s.RunStorageCompression(ctx)
	}
}

func (s *Server) handleStorageCompression(w http.ResponseWriter, r *http.Request) {
	cs := s.compression()
	cs.mu.Lock()
	out := map[string]any{
		"enabled":      s.settings.Get().StorageCompression,
		"running":      cs.running,
		"extract_idle": int64(storage.ExtractIdle / time.Second),
	}
	if cs.running {
		cur := cs.current
		out["current"] = cur
	}
	if cs.last != nil {
		last := *cs.last
		out["last"] = last
		out["last_error"] = cs.lastErr
	}
	cs.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// handleStorageCompressionRun starts a pass now (also when the setting is
// off: a one-off compression of what is stored).
func (s *Server) handleStorageCompressionRun(w http.ResponseWriter, r *http.Request) {
	cs := s.compression()
	cs.mu.Lock()
	running := cs.running
	cs.mu.Unlock()
	if running {
		writeErr(w, r, errf(http.StatusConflict, "already_running", "A compression pass is already running"))
		return
	}
	s.activity(r, principal(r).User.ID, "storage_compression", "storage", "started a storage compression pass", true)
	go s.RunStorageCompression(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// uploadCompressor compresses freshly stored uploads one at a time, so a
// burst of uploads does not occupy every core.
type uploadCompressor struct {
	once sync.Once
	jobs chan storage.UploadedFile
}

var uploadCompressors sync.Map // *Server -> *uploadCompressor

// compressUploaded queues the stored blob of a finished upload for
// compression when storage compression is on (reusing the client's frames)
// and otherwise drops the upload's kept frames.
func (s *Server) compressUploaded(up *store.Upload) {
	tmp, err := s.blobs.TempPath(up.LocationID, up.ID)
	if err != nil {
		return
	}
	frames := storage.FramesDir(tmp)
	if !s.settings.Get().StorageCompression || up.SHA256 == "" {
		_ = os.RemoveAll(frames)
		return
	}
	v, _ := uploadCompressors.LoadOrStore(s, &uploadCompressor{jobs: make(chan storage.UploadedFile, 256)})
	uc := v.(*uploadCompressor)
	uc.once.Do(func() {
		go func() {
			for f := range uc.jobs {
				done, err := s.blobs.CompressUploaded(context.Background(), f)
				if err != nil {
					slog.Warn("compressing an uploaded file failed; the background pass retries it", "sha256", f.SHA256, "err", err)
				} else if done {
					slog.Debug("uploaded file stored compressed", "sha256", f.SHA256, "client_compressed", f.ClientCompressed)
				}
			}
		}()
	})
	f := storage.UploadedFile{UserID: up.UserID, SHA256: up.SHA256, Size: up.Size, ChunkSize: up.ChunkSize, Frames: frames, ClientCompressed: up.ClientCompressed}
	select {
	case uc.jobs <- f:
	default:
		_ = os.RemoveAll(frames) // busy: the background pass compresses it later
	}
}

func init() { implementFeature(wire.FeatureUploadCompressedFlag) }

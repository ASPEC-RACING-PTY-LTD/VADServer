package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type UploadRequest struct {
	Path       string   `json:"path"`
	Size       int64    `json:"size"`
	SHA256     string   `json:"sha256"`
	ModifiedAt int64    `json:"modified_at"`
	Tags       []string `json:"tags,omitempty"`
	OnConflict string   `json:"on_conflict,omitempty"`
}

type UploadSession struct {
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

// ChunkLen returns the length of chunk i.
func (u *UploadSession) ChunkLen(i int64) int64 {
	return min(u.ChunkSize, u.Size-i*u.ChunkSize)
}

// Missing returns chunk indices not yet received.
func (u *UploadSession) Missing() []int64 {
	have := map[int64]bool{}
	for _, i := range u.Received {
		have[i] = true
	}
	var out []int64
	for i := int64(0); i < u.ChunksTotal; i++ {
		if !have[i] {
			out = append(out, i)
		}
	}
	return out
}

type UploadResult struct {
	Status  string          `json:"status"` // "open" or "complete"
	Result  string          `json:"result"` // created, replaced, renamed, unchanged, skipped, deduplicated
	Resumed bool            `json:"resumed"`
	Upload  *UploadSession  `json:"upload"`
	Node    json.RawMessage `json:"node"`
}

func (c *Client) CreateUpload(ctx context.Context, req UploadRequest) (*UploadResult, error) {
	var out UploadResult
	return &out, c.Call(ctx, "POST", "/api/v1/uploads", req, &out)
}

func (c *Client) GetUpload(ctx context.Context, id string) (*UploadSession, error) {
	var out struct {
		Upload UploadSession `json:"upload"`
	}
	return &out.Upload, c.Call(ctx, "GET", "/api/v1/uploads/"+id, nil, &out)
}

func (c *Client) CancelUpload(ctx context.Context, id string) error {
	return c.Call(ctx, "DELETE", "/api/v1/uploads/"+id, nil, nil)
}

func (c *Client) CompleteUpload(ctx context.Context, id string) (*UploadResult, error) {
	var out UploadResult
	return &out, c.Call(ctx, "POST", "/api/v1/uploads/"+id+"/complete", nil, &out)
}

// HashSection returns the SHA-256 of length bytes at off.
func HashSection(r io.ReaderAt, off, length int64) (string, error) {
	h := sha256.New()
	if _, err := io.CopyBuffer(h, io.NewSectionReader(r, off, length), make([]byte, 256<<10)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PutChunk uploads one chunk streamed from r. The chunk is hashed first so
// the hash can be signed; then it is streamed without buffering in memory.
func (c *Client) PutChunk(ctx context.Context, id string, idx int64, r io.ReaderAt, off, length int64) error {
	sum, err := HashSection(r, off, length)
	if err != nil {
		return fmt.Errorf("read chunk: %w", err)
	}
	return c.PutChunkHashed(ctx, id, idx, r, off, length, sum)
}

// PutChunkHashed uploads a chunk whose hash is already known.
func (c *Client) PutChunkHashed(ctx context.Context, id string, idx int64, r io.ReaderAt, off, length int64, sum string) error {
	wrap := func(rd io.Reader) io.Reader {
		if c.ChunkBody != nil {
			return c.ChunkBody(rd)
		}
		return rd
	}
	body := wrap(io.NewSectionReader(r, off, length))
	req, err := c.newRequest(ctx, "PUT", fmt.Sprintf("/api/v1/uploads/%s/chunks/%d", id, idx), body, sum, length, true)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Chunk-SHA256", sum)
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(wrap(io.NewSectionReader(r, off, length))), nil }
	return c.send(req, nil)
}

// HashFile computes a file's SHA-256.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UploadFile uploads a local file sequentially with resume and retries.
// It is a simple reference implementation; the desktop sync engine uses its
// own scheduler with adaptive concurrency on top of the same primitives.
func (c *Client) UploadFile(ctx context.Context, localPath, remotePath string, tags []string) (*UploadResult, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	sum, err := HashFile(localPath)
	if err != nil {
		return nil, err
	}
	var res *UploadResult
	err = retry(ctx, 6, func() error {
		var e error
		res, e = c.CreateUpload(ctx, UploadRequest{Path: remotePath, Size: st.Size(), SHA256: sum, ModifiedAt: st.ModTime().UnixMilli(), Tags: tags})
		return e
	})
	if err != nil {
		return nil, err
	}
	if res.Status == "complete" {
		return res, nil
	}
	up := res.Upload
	for _, idx := range up.Missing() {
		idx := idx
		if err := retry(ctx, 8, func() error { return c.PutChunk(ctx, up.ID, idx, f, idx*up.ChunkSize, up.ChunkLen(idx)) }); err != nil {
			return nil, err
		}
	}
	var done *UploadResult
	err = retry(ctx, 6, func() error {
		var e error
		done, e = c.CompleteUpload(ctx, up.ID)
		return e
	})
	return done, err
}

func retry(ctx context.Context, attempts int, fn func() error) error {
	delay := 500 * time.Millisecond
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil || !Retryable(err) {
			return err
		}
		var ae *APIError
		wait := delay
		if errors.As(err, &ae) && ae.RetryAfter > 0 {
			wait = ae.RetryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		delay = min(delay*2, 30*time.Second)
	}
	return err
}

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// --- repositories (api-v1 §5) ---

// CreateRepo creates a repository, or returns the existing one with the same
// repo_uuid. FormatVersion defaults to wire.RepoFormatVersion.
func (c *Client) CreateRepo(ctx context.Context, req wire.RepoCreateRequest) (*wire.Repo, error) {
	if req.FormatVersion == 0 {
		req.FormatVersion = wire.RepoFormatVersion
	}
	var out wire.Repo
	if err := c.Call(ctx, "POST", apiPath("/repos", nil), req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Repos lists the repositories visible to this device.
func (c *Client) Repos(ctx context.Context, cursor string, limit int) (*wire.List[wire.Repo], error) {
	var out wire.List[wire.Repo]
	if err := c.Call(ctx, "GET", apiPath("/repos", listQuery(cursor, limit)), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Repo returns one repository.
func (c *Client) Repo(ctx context.Context, id int64) (*wire.Repo, error) {
	var out wire.Repo
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/repos/%d", id), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- objects ---

func objectPath(repoID int64, kind, oid string) string {
	return apiPath(fmt.Sprintf("/repos/%d/objects/%s/%s", repoID, url.PathEscape(kind), url.PathEscape(oid)), nil)
}

// HeadObject reports whether an object exists and its size. A 404 is
// (false, 0, nil).
func (c *Client) HeadObject(ctx context.Context, repoID int64, kind, oid string) (bool, int64, error) {
	req, err := c.newRequest(ctx, "HEAD", objectPath(repoID, kind, oid), nil, protocol.EmptySHA256, 0, true)
	if err != nil {
		return false, 0, err
	}
	resp, err := c.open(req)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return false, 0, nil
		}
		return false, 0, err
	}
	resp.Body.Close()
	return true, resp.ContentLength, nil
}

// PutObject streams an object body to the repository (a streamed route,
// api-v1 §1.2). size is the exact body length and sha256Hex the hex SHA-256
// of the body, which is signed and sent as X-VD-Content-SHA256; when empty
// it defaults to oid (every kind except config). The body is never buffered:
// it is hashed while sent, and a body whose hash or length differs aborts
// the request before it completes. created is false when the object
// already existed (200).
func (c *Client) PutObject(ctx context.Context, repoID int64, kind, oid string, body io.Reader, size int64, sha256Hex string) (created bool, err error) {
	if sha256Hex == "" {
		if kind == wire.ObjectKindConfig {
			return false, errors.New("the config object needs an explicit body SHA-256")
		}
		sha256Hex = oid
	}
	st, err := c.sendStreamed(ctx, "PUT", objectPath(repoID, kind, oid), "application/octet-stream", body, size, sha256Hex, nil)
	if err != nil {
		return false, err
	}
	return st == http.StatusCreated, nil
}

// ErrBodyMismatch is returned when a streamed body does not match its
// declared SHA-256 or length.
var ErrBodyMismatch = errors.New("the streamed body does not match its declared SHA-256 or length")

// sendStreamed signs and sends a body of known length and hash without
// buffering it.
func (c *Client) sendStreamed(ctx context.Context, method, path, contentType string, body io.Reader, size int64, sha256Hex string, out any) (int, error) {
	sha256Hex = strings.ToLower(sha256Hex)
	if b, err := hex.DecodeString(sha256Hex); err != nil || len(b) != sha256.Size {
		return 0, errors.New("body SHA-256 must be 64 hex characters")
	}
	if size < 0 {
		return 0, errors.New("body size must be known")
	}
	var rdr io.Reader = http.NoBody
	if size > 0 {
		rdr = &verifyingReader{r: body, h: sha256.New(), want: sha256Hex, left: size}
	} else if sha256Hex != protocol.EmptySHA256 {
		return 0, ErrBodyMismatch
	}
	req, err := c.newRequest(ctx, method, path, rdr, sha256Hex, size, true)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.sendStatus(req, out)
}

// verifyingReader hashes a body while it is read and fails the final read
// when the hash or length differs from the declared values, so the HTTP
// transport aborts the request instead of completing it.
type verifyingReader struct {
	r    io.Reader
	h    hash.Hash
	want string
	left int64
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	if v.left <= 0 {
		// Reject trailing bytes beyond the declared size.
		var one [1]byte
		if n, _ := v.r.Read(one[:]); n > 0 {
			return 0, ErrBodyMismatch
		}
		return 0, io.EOF
	}
	if int64(len(p)) > v.left {
		p = p[:v.left]
	}
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	v.left -= int64(n)
	if errors.Is(err, io.EOF) {
		if v.left > 0 {
			return n, ErrBodyMismatch
		}
		err = nil
	}
	if err == nil && v.left == 0 && hex.EncodeToString(v.h.Sum(nil)) != v.want {
		return n, ErrBodyMismatch
	}
	return n, err
}

// GetObject opens an object for reading. offset > 0 or length > 0 send an
// HTTP Range (length <= 0 reads to the end). The caller closes the reader.
func (c *Client) GetObject(ctx context.Context, repoID int64, kind, oid string, offset, length int64) (io.ReadCloser, error) {
	req, err := c.newRequest(ctx, "GET", objectPath(repoID, kind, oid), nil, protocol.EmptySHA256, 0, true)
	if err != nil {
		return nil, err
	}
	if offset > 0 || length > 0 {
		end := ""
		if length > 0 {
			end = strconv.FormatInt(offset+length-1, 10)
		}
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+end)
	}
	resp, err := c.open(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ListObjects lists object ids and sizes of one kind (kind may be empty).
func (c *Client) ListObjects(ctx context.Context, repoID int64, kind, cursor string, limit int) (*wire.List[wire.ObjectInfo], error) {
	q := listQuery(cursor, limit)
	if kind != "" {
		q.Set("kind", kind)
	}
	var out wire.List[wire.ObjectInfo]
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/repos/%d/objects", repoID), q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MissingObjects returns the oids the server does not have, querying in
// batches of at most wire.ObjectMissingMaxOIDs.
func (c *Client) MissingObjects(ctx context.Context, repoID int64, kind string, oids []string) ([]string, error) {
	missing := []string{}
	for len(oids) > 0 {
		n := min(len(oids), wire.ObjectMissingMaxOIDs)
		var out wire.ObjectMissingResponse
		if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/repos/%d/objects/missing", repoID), nil), wire.ObjectMissingRequest{Kind: kind, OIDs: oids[:n]}, &out); err != nil {
			return nil, err
		}
		missing = append(missing, out.Missing...)
		oids = oids[n:]
	}
	return missing, nil
}

// --- locks ---

// AcquireLock takes a shared or exclusive repository lock. An incompatible
// lock returns 409 repo_locked; RepoLocked(err) gives the holder.
func (c *Client) AcquireLock(ctx context.Context, repoID int64, kind string, ttlSeconds int) (*wire.Lock, error) {
	var out wire.Lock
	if err := c.Call(ctx, "POST", apiPath(fmt.Sprintf("/repos/%d/locks", repoID), nil), wire.LockRequest{Kind: kind, TTLSeconds: ttlSeconds}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefreshLock extends a lock. ttlSeconds > 0 is sent as {"ttl_seconds"};
// otherwise there is no body. It returns the new lock state when the
// server replies with one, else nil.
func (c *Client) RefreshLock(ctx context.Context, repoID int64, lockID string, ttlSeconds int) (*wire.Lock, error) {
	var in any
	if ttlSeconds > 0 {
		in = map[string]int{"ttl_seconds": ttlSeconds}
	}
	var out wire.Lock
	if err := c.Call(ctx, "PUT", apiPath(fmt.Sprintf("/repos/%d/locks/%s", repoID, url.PathEscape(lockID)), nil), in, &out); err != nil {
		return nil, err
	}
	if out.LockID == "" && out.ExpiresAt == 0 {
		return nil, nil
	}
	return &out, nil
}

// ReleaseLock releases a lock.
func (c *Client) ReleaseLock(ctx context.Context, repoID int64, lockID string) error {
	return c.Call(ctx, "DELETE", apiPath(fmt.Sprintf("/repos/%d/locks/%s", repoID, url.PathEscape(lockID)), nil), nil, nil)
}

// --- snapshots ---

// CommitSnapshot commits a snapshot (api-v1 §5.2, a streamed route). The
// JSON body is encoded twice, once to hash and measure it and once while
// streaming, so it is never held in memory in full. created is false on an
// idempotent replay (200).
func (c *Client) CommitSnapshot(ctx context.Context, repoID int64, req wire.SnapshotCommitRequest) (resp *wire.SnapshotCommitResponse, created bool, err error) {
	hc := &hashCounter{h: sha256.New()}
	if err := json.NewEncoder(hc).Encode(req); err != nil {
		return nil, false, err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(json.NewEncoder(pw).Encode(req)) }()
	defer pr.Close()
	var out wire.SnapshotCommitResponse
	st, err := c.sendStreamed(ctx, "POST", apiPath(fmt.Sprintf("/repos/%d/snapshots", repoID), nil), "application/json", pr, hc.n, hex.EncodeToString(hc.h.Sum(nil)), &out)
	if err != nil {
		return nil, false, err
	}
	return &out, st == http.StatusCreated, nil
}

type hashCounter struct {
	h hash.Hash
	n int64
}

func (w *hashCounter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return w.h.Write(p)
}

// SnapshotQuery filters Snapshots (api-v1 §5.4). Zero values are omitted.
type SnapshotQuery struct {
	RepoID, JobID, DeviceID int64
	Profile, Kind, Tag      string
	From, To                int64
	Protected               *bool
	Cursor                  string
	Limit                   int
}

func (q SnapshotQuery) values() url.Values {
	v := listQuery(q.Cursor, q.Limit)
	for k, n := range map[string]int64{"repo": q.RepoID, "job": q.JobID, "device": q.DeviceID, "from": q.From, "to": q.To} {
		if n != 0 {
			v.Set(k, strconv.FormatInt(n, 10))
		}
	}
	for k, s := range map[string]string{"profile": q.Profile, "kind": q.Kind, "tag": q.Tag} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.Protected != nil {
		v.Set("protected", strconv.FormatBool(*q.Protected))
	}
	return v
}

// Snapshots lists catalog snapshots.
func (c *Client) Snapshots(ctx context.Context, q SnapshotQuery) (*wire.List[wire.Snapshot], error) {
	var out wire.List[wire.Snapshot]
	if err := c.Call(ctx, "GET", apiPath("/snapshots", q.values()), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Snapshot returns one catalog snapshot.
func (c *Client) Snapshot(ctx context.Context, id int64) (*wire.Snapshot, error) {
	var out wire.Snapshot
	if err := c.Call(ctx, "GET", apiPath(fmt.Sprintf("/snapshots/%d", id), nil), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

package nodeagent

// Jobs from the server: the node long-polls for work, so it needs no
// inbound access. Copies arrive encrypted by the server and are stored as
// they are; the node never sees file contents.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
)

const locationMarker = ".vad-node-location"

// locations maps node location ids to their folders (data/locations.json).
type locations struct {
	mu   sync.Mutex
	path string
	m    map[int64]string
}

func (a *Agent) locs() *locations {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locations == nil {
		l := &locations{path: filepath.Join(a.cfg.DataDir(), "locations.json"), m: map[int64]string{}}
		if b, err := os.ReadFile(l.path); err == nil {
			_ = json.Unmarshal(b, &l.m)
		}
		a.locations = l
	}
	return a.locations
}

func (l *locations) get(id int64) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.m[id]
	return p, ok
}

func (l *locations) set(id int64, p string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[id] = p
	b, _ := json.MarshalIndent(l.m, "", "  ")
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

func (l *locations) all() map[int64]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[int64]string, len(l.m))
	for k, v := range l.m {
		out[k] = v
	}
	return out
}

// locationStatus reports the prepared locations for the heartbeat.
func (a *Agent) locationStatus() []nodeapi.LocationStatus {
	var out []nodeapi.LocationStatus
	for id, p := range a.locs().all() {
		st := nodeapi.LocationStatus{ID: id, Path: p}
		if _, err := os.Stat(filepath.Join(p, locationMarker)); err != nil {
			st.Error = "the folder is missing or not mounted"
		} else {
			st.OK = true
		}
		st.DiskTotal, st.DiskFree = diskSpace(p)
		out = append(out, st)
	}
	return out
}

func (a *Agent) allowedRoot(p string) bool {
	if len(a.cfg.StorageRoots) == 0 {
		return true
	}
	p = filepath.Clean(p)
	for _, r := range a.cfg.StorageRoots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// copyPath is the file of a blob's copy in a prepared location.
func (a *Agent) copyPath(p nodeapi.JobPayload) (string, error) {
	root, ok := a.locs().get(p.LocationID)
	if !ok {
		return "", fmt.Errorf("location %d is not prepared on this node", p.LocationID)
	}
	if !nodeapi.ValidSHA256(p.SHA256) || p.UserID <= 0 {
		return "", errors.New("invalid copy name")
	}
	return filepath.Join(root, filepath.FromSlash(nodeapi.CopyFileName(p.UserID, p.SHA256))), nil
}

// jobLoop pulls and runs jobs until ctx ends.
func (a *Agent) jobLoop(ctx context.Context, auth map[string]string) {
	poll := &http.Client{Timeout: 60 * time.Second}
	sem := make(chan struct{}, 4)
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.ServerURL+"/api/v1/node/jobs/next", nil)
		for k, v := range auth {
			req.Header.Set(k, v)
		}
		req.Header.Set("User-Agent", "vad-node/"+Version)
		resp, err := poll.Do(req)
		if err != nil {
			sleep(ctx, 5*time.Second)
			continue
		}
		var job nodeapi.Job
		code := resp.StatusCode
		if code == 200 {
			err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&job)
		}
		resp.Body.Close()
		switch {
		case code == 204:
			continue
		case code != 200 || err != nil:
			sleep(ctx, 10*time.Second)
			continue
		}
		if job.Kind == nodeapi.JobTerminal {
			go a.runJob(ctx, auth, job)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		go func() {
			defer func() { <-sem }()
			a.runJob(ctx, auth, job)
		}()
	}
}

func (a *Agent) runJob(ctx context.Context, auth map[string]string, job nodeapi.Job) {
	var res nodeapi.JobResult
	var err error
	switch job.Kind {
	case nodeapi.JobPrepare:
		res, err = a.jobPrepare(job.Payload)
	case nodeapi.JobPut:
		res, err = a.jobPut(ctx, auth, job)
	case nodeapi.JobGet:
		res, err = a.jobGet(ctx, auth, job)
	case nodeapi.JobVerify:
		res, err = a.jobVerify(job.Payload)
	case nodeapi.JobDelete:
		res, err = a.jobDelete(job.Payload)
	case nodeapi.JobTerminal:
		err = a.startTerminal(ctx, auth, job.Payload)
		res.OK = err == nil
	default:
		err = fmt.Errorf("unknown job kind %q (update this node)", job.Kind)
	}
	if err != nil {
		res = nodeapi.JobResult{OK: false, Error: err.Error()}
		a.log.Warn("job failed", "kind", job.Kind, "job", job.ID, "err", err)
	}
	for i := 0; i < 5 && ctx.Err() == nil; i++ {
		if _, perr := a.do(ctx, "POST", fmt.Sprintf("/api/v1/node/jobs/%d/result", job.ID), res, auth, nil); perr == nil {
			return
		}
		sleep(ctx, time.Duration(i+1)*2*time.Second)
	}
}

func (a *Agent) jobPrepare(p nodeapi.JobPayload) (nodeapi.JobResult, error) {
	dir := filepath.Clean(p.Path)
	if !filepath.IsAbs(dir) {
		return nodeapi.JobResult{}, fmt.Errorf("%q is not an absolute folder", p.Path)
	}
	if !a.allowedRoot(dir) {
		return nodeapi.JobResult{}, fmt.Errorf("%s is outside the folders this node allows (VAD_NODE_STORAGE_ROOTS=%s)", dir, strings.Join(a.cfg.StorageRoots, ","))
	}
	if err := os.MkdirAll(filepath.Join(dir, "replicas"), 0o750); err != nil {
		return nodeapi.JobResult{}, err
	}
	marker := filepath.Join(dir, locationMarker)
	if err := os.WriteFile(marker, []byte("VaultDock node location "+strconv.FormatInt(p.LocationID, 10)+". Files here are encrypted copies; do not edit them.\n"), 0o640); err != nil {
		return nodeapi.JobResult{}, fmt.Errorf("cannot write in %s: %w", dir, err)
	}
	if err := a.locs().set(p.LocationID, dir); err != nil {
		return nodeapi.JobResult{}, err
	}
	total, free := diskSpace(dir)
	a.log.Info("location prepared", "id", p.LocationID, "path", dir)
	return nodeapi.JobResult{OK: true, DiskTotal: total, DiskFree: free}, nil
}

func (a *Agent) jobPut(ctx context.Context, auth map[string]string, job nodeapi.Job) (nodeapi.JobResult, error) {
	dst, err := a.copyPath(job.Payload)
	if err != nil {
		return nodeapi.JobResult{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/v1/node/jobs/%d/data", a.cfg.ServerURL, job.ID), nil)
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nodeapi.JobResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nodeapi.JobResult{}, fmt.Errorf("download: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return nodeapi.JobResult{}, err
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return nodeapi.JobResult{}, err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), resp.Body, make([]byte, 1<<20))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return nodeapi.JobResult{}, err
	}
	total, free := diskSpace(dst)
	return nodeapi.JobResult{OK: true, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, DiskTotal: total, DiskFree: free}, nil
}

func (a *Agent) jobGet(ctx context.Context, auth map[string]string, job nodeapi.Job) (nodeapi.JobResult, error) {
	src, err := a.copyPath(job.Payload)
	if err != nil {
		return nodeapi.JobResult{}, err
	}
	f, err := os.Open(src)
	if err != nil {
		return nodeapi.JobResult{}, fmt.Errorf("the copy is missing on this node: %w", err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	req, _ := http.NewRequestWithContext(ctx, "PUT", fmt.Sprintf("%s/api/v1/node/jobs/%d/data", a.cfg.ServerURL, job.ID), f)
	req.ContentLength = fi.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nodeapi.JobResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nodeapi.JobResult{}, fmt.Errorf("upload: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(bytes.TrimSpace(b))))
	}
	return nodeapi.JobResult{OK: true, Size: fi.Size()}, nil
}

func (a *Agent) jobVerify(p nodeapi.JobPayload) (nodeapi.JobResult, error) {
	src, err := a.copyPath(p)
	if err != nil {
		return nodeapi.JobResult{}, err
	}
	f, err := os.Open(src)
	if err != nil {
		return nodeapi.JobResult{OK: false, Error: "the copy is missing"}, nil
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 1<<20))
	if err != nil {
		return nodeapi.JobResult{OK: false, Error: "read failed: " + err.Error()}, nil
	}
	return nodeapi.JobResult{OK: true, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

func (a *Agent) jobDelete(p nodeapi.JobPayload) (nodeapi.JobResult, error) {
	src, err := a.copyPath(p)
	if err != nil {
		if _, ok := a.locs().get(p.LocationID); !ok {
			return nodeapi.JobResult{OK: true}, nil // location gone: nothing to delete
		}
		return nodeapi.JobResult{}, err
	}
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return nodeapi.JobResult{}, err
	}
	return nodeapi.JobResult{OK: true}, nil
}

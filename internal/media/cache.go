package media

import (
	"context"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Cache keeps generated thumbnails, frames and stream information on disk,
// keyed by user and content hash: a file is processed once whatever it is
// named or wherever it is moved, and a changed file (new hash) gets new
// ones.
//
//	<dir>/<user>/<sha[0:2]>/<sha>/poster.jpg   thumbnail for the file browser
//	                              info.json    stream information
//	                              failed       why the file could not be read
//	                              frames/<h>/<ms>.jpg   timeline frames
type Cache struct{ Dir string }

// FailedRetry: a file that could not be read is tried again after this
// long (it may have been a timeout under load).
const FailedRetry = 6 * time.Hour

// FrameIdle: timeline frames unused this long are removed.
const FrameIdle = 7 * 24 * time.Hour

func (c Cache) dir(userID int64, sha string) string {
	return filepath.Join(c.Dir, strconv.FormatInt(userID, 10), sha[:2], sha)
}

// PosterPath is where the file browser thumbnail lives.
func (c Cache) PosterPath(userID int64, sha string) string {
	return filepath.Join(c.dir(userID, sha), "poster.jpg")
}

// FramePath is where a timeline frame lives.
func (c Cache) FramePath(userID int64, sha string, height int, ms int64) string {
	return filepath.Join(c.dir(userID, sha), "frames", strconv.Itoa(height), strconv.FormatInt(ms, 10)+".jpg")
}

// Info returns cached stream information.
func (c Cache) Info(userID int64, sha string) (*Info, bool) {
	b, err := os.ReadFile(filepath.Join(c.dir(userID, sha), "info.json"))
	if err != nil {
		return nil, false
	}
	var i Info
	if json.Unmarshal(b, &i) != nil {
		return nil, false
	}
	return &i, true
}

// PutInfo stores stream information.
func (c Cache) PutInfo(userID int64, sha string, i *Info) error {
	b, err := json.Marshal(i)
	if err != nil {
		return err
	}
	return c.write(filepath.Join(c.dir(userID, sha), "info.json"), b)
}

// Failed returns why the file could not be processed, if it failed
// recently.
func (c Cache) Failed(userID int64, sha string) (string, bool) {
	p := filepath.Join(c.dir(userID, sha), "failed")
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > FailedRetry {
		return "", false
	}
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b)), true
}

// PutFailed records a failure.
func (c Cache) PutFailed(userID int64, sha, reason string) {
	_ = c.write(filepath.Join(c.dir(userID, sha), "failed"), []byte(reason))
}

// ClearFailed forgets a failure.
func (c Cache) ClearFailed(userID int64, sha string) {
	_ = os.Remove(filepath.Join(c.dir(userID, sha), "failed"))
}

// Put stores generated data at p atomically.
func (c Cache) Put(p string, b []byte) error { return c.write(p, b) }

func (c Cache) write(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp := p + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Touch marks a cached file as used (at most once a day, to spare writes).
func Touch(p string, fi os.FileInfo) {
	if time.Since(fi.ModTime()) > 24*time.Hour {
		t := time.Now()
		_ = os.Chtimes(p, t, t)
	}
}

// PosterTime picks where the thumbnail is taken: a little way in, past
// intros and black frames, but never far into long recordings.
func PosterTime(duration float64) float64 {
	if duration <= 2 {
		return 0
	}
	return math.Min(duration*0.1, 8)
}

// Sweep removes cache entries whose content is no longer stored
// (exists reports it) and timeline frames unused for FrameIdle.
func (c Cache) Sweep(ctx context.Context, exists func(userID int64, sha string) bool) (removed int) {
	users, _ := os.ReadDir(c.Dir)
	for _, u := range users {
		uid, err := strconv.ParseInt(u.Name(), 10, 64)
		if err != nil || !u.IsDir() {
			continue
		}
		prefixes, _ := os.ReadDir(filepath.Join(c.Dir, u.Name()))
		for _, p := range prefixes {
			entries, _ := os.ReadDir(filepath.Join(c.Dir, u.Name(), p.Name()))
			for _, e := range entries {
				if ctx.Err() != nil {
					return removed
				}
				sha := e.Name()
				d := filepath.Join(c.Dir, u.Name(), p.Name(), sha)
				if len(sha) != 64 || !exists(uid, sha) {
					if os.RemoveAll(d) == nil {
						removed++
					}
					continue
				}
				_ = filepath.WalkDir(filepath.Join(d, "frames"), func(fp string, de fs.DirEntry, err error) error {
					if err != nil || de.IsDir() {
						return nil
					}
					if fi, err := de.Info(); err == nil && time.Since(fi.ModTime()) > FrameIdle {
						if os.Remove(fp) == nil {
							removed++
						}
					}
					return nil
				})
			}
		}
	}
	return removed
}

package deployenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// ApprovalsFile is the approvals file below the state directory (/state in
// the container, ${VAULTDOCK_STATE_PATH} on the host), api-v1 §11A.
const ApprovalsFile = "deploy/env-removal-approvals.json"

// maxApprovalsSize bounds what Load reads; the file holds a few names.
const maxApprovalsSize = 1 << 20

// Approvals is the admin removal approvals file that install.sh upgrade
// applies once and then deletes. Format: a JSON array of flat objects
// {"name","approved_by","approved_at"} (install.sh reads the name of every
// flat object). Writes are atomic (temp file + rename).
type Approvals struct {
	path string
	mu   sync.Mutex
}

// NewApprovals returns the approvals file of the given state directory.
func NewApprovals(stateDir string) *Approvals {
	return &Approvals{path: filepath.Join(stateDir, filepath.FromSlash(ApprovalsFile))}
}

// Path is the file's location.
func (a *Approvals) Path() string { return a.path }

// List returns the pending approvals (empty when the file does not exist).
func (a *Approvals) List() ([]wire.DeployEnvRemovalApproval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.load()
}

// Approve adds approvals for names (an already approved name keeps its
// original approval) and returns the full pending list.
func (a *Approvals) Approve(names []string, by, at int64) ([]wire.DeployEnvRemovalApproval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list, err := a.load()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if !ValidName(n) {
			return nil, fmt.Errorf("deployenv: invalid variable name %q", n)
		}
		if indexOf(list, n) < 0 {
			list = append(list, wire.DeployEnvRemovalApproval{Name: n, ApprovedBy: by, ApprovedAt: at})
		}
	}
	return list, a.save(list)
}

// Withdraw removes the approval for name. It reports false when name was not
// pending. The file is deleted when no approval remains.
func (a *Approvals) Withdraw(name string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list, err := a.load()
	if err != nil {
		return false, err
	}
	i := indexOf(list, name)
	if i < 0 {
		return false, nil
	}
	list = append(list[:i], list[i+1:]...)
	return true, a.save(list)
}

func indexOf(list []wire.DeployEnvRemovalApproval, name string) int {
	for i, x := range list {
		if x.Name == name {
			return i
		}
	}
	return -1
}

// load reads the file. Both a bare array and {"approvals":[…]} (accepted by
// install.sh too) are understood.
func (a *Approvals) load() ([]wire.DeployEnvRemovalApproval, error) {
	f, err := os.Open(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		return []wire.DeployEnvRemovalApproval{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, maxApprovalsSize+1)); err != nil {
		return nil, err
	}
	if buf.Len() > maxApprovalsSize {
		return nil, errors.New("deployenv: approvals file too large")
	}
	b := bytes.TrimSpace(buf.Bytes())
	list := []wire.DeployEnvRemovalApproval{}
	if len(b) == 0 {
		return list, nil
	}
	if b[0] == '{' {
		var wrap struct {
			Approvals []wire.DeployEnvRemovalApproval `json:"approvals"`
		}
		if err := json.Unmarshal(b, &wrap); err != nil {
			return nil, fmt.Errorf("deployenv: parse %s: %w", a.path, err)
		}
		list = append(list, wrap.Approvals...)
	} else if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("deployenv: parse %s: %w", a.path, err)
	}
	out := list[:0]
	for _, x := range list {
		if ValidName(x.Name) && indexOf(out, x.Name) < 0 {
			out = append(out, x)
		}
	}
	return out, nil
}

// save writes list atomically, or removes the file when list is empty.
func (a *Approvals) save(list []wire.DeployEnvRemovalApproval) error {
	if len(list) == 0 {
		if err := os.Remove(a.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(a.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".env-removal-approvals-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(b) // CreateTemp makes the file 0600
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, a.path)
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync() // best effort; not supported on every platform
		d.Close()
	}
	return nil
}

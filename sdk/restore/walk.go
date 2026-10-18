package restore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/snapshot"
)

// TreeReader reads tree blobs by hex id (*repo.Repo implements it, also
// for metadata-only holders).
type TreeReader interface {
	ReadTree(ctx context.Context, idHex string) (*snapshot.Tree, error)
}

// Errors of the tree walk.
var (
	ErrNotFound = errors.New("restore: path not in snapshot")
	ErrTooDeep  = errors.New("restore: tree nesting too deep")
)

// maxDepth bounds directory nesting (and continuation chains) so a
// malformed or hostile repository cannot recurse forever.
const maxDepth = 1024

// Item is one snapshot entry at a token path.
type Item struct {
	Path  string // token path, e.g. "{DOCUMENTS}/Reports/q3.xlsx"
	Entry snapshot.Entry
}

// IsDir reports whether the item is a directory.
func (it Item) IsDir() bool { return it.Entry.T == snapshot.TypeDir }

// entries reads a tree and its continuation blobs.
func entries(ctx context.Context, tr TreeReader, id string, fn func(snapshot.Entry) error) error {
	for n := 0; id != ""; n++ {
		if n > maxDepth*64 {
			return ErrTooDeep
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		t, err := tr.ReadTree(ctx, id)
		if err != nil {
			return err
		}
		for _, e := range t.Entries {
			if err := fn(e); err != nil {
				return err
			}
		}
		id = ""
		if t.Cont != nil {
			id = *t.Cont
		}
	}
	return nil
}

// Walk calls fn for every selected entry under the snapshot roots, parents
// before children. Directories are reported when selected (so a restore can
// create them and set their times); unselected directories are only
// entered when an include rule lies below them. Returning an error from fn
// stops the walk with that error.
func Walk(ctx context.Context, tr TreeReader, roots []snapshot.RootRef, sel *Selector, fn func(Item) error) error {
	if sel == nil {
		sel = &Selector{}
	}
	for _, r := range roots {
		if !sel.Descend(r.TokenPath) {
			continue
		}
		if err := walkTree(ctx, tr, r.TokenPath, r.Tree, sel, fn, 0); err != nil {
			return err
		}
	}
	return nil
}

func walkTree(ctx context.Context, tr TreeReader, dir, id string, sel *Selector, fn func(Item) error, depth int) error {
	if depth > maxDepth {
		return ErrTooDeep
	}
	return entries(ctx, tr, id, func(e snapshot.Entry) error {
		if e.N == "" || strings.ContainsAny(e.N, "/\\") {
			return nil // names that cannot form a path (non-UTF-8 "nb" names are not restored)
		}
		p := dir + "/" + e.N
		if e.T == snapshot.TypeDir {
			if sel.Selected(p) {
				if err := fn(Item{Path: p, Entry: e}); err != nil {
					return err
				}
			}
			if e.Sub != nil && sel.Descend(p) {
				return walkTree(ctx, tr, p, *e.Sub, sel, fn, depth+1)
			}
			return nil
		}
		if sel.Selected(p) {
			return fn(Item{Path: p, Entry: e})
		}
		return nil
	})
}

// ListDir lists one directory of the snapshot. An empty path lists the top
// level: the snapshot's roots, with intermediate token paths shown as
// directories. Entries come back directories first, then by name.
func ListDir(ctx context.Context, tr TreeReader, roots []snapshot.RootRef, path string) ([]Item, error) {
	path = strings.TrimSuffix(path, "/")
	var target []string
	if path != "" {
		c, err := splitPath(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		target = c
	}
	var out []Item
	virtual := map[string]bool{}
	found := false
	for _, r := range roots {
		rc, err := splitPath(r.TokenPath)
		if err != nil {
			continue
		}
		switch {
		case len(rc) > len(target) && prefixOf(target, rc):
			// The root lies below the listed path: show the next component
			// as a directory.
			name := originalComponent(r.TokenPath, len(target))
			key := strings.ToLower(name)
			if !virtual[key] {
				virtual[key] = true
				p := name
				if path != "" {
					p = path + "/" + name
				}
				out = append(out, Item{Path: p, Entry: snapshot.Entry{N: name, T: snapshot.TypeDir}})
			}
			found = true
		case prefixOf(rc, target):
			items, err := listBelow(ctx, tr, r, rc, target, path)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			found = true
		}
	}
	if !found && path != "" {
		return nil, ErrNotFound
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir() != out[j].IsDir() {
			return out[i].IsDir()
		}
		return strings.ToLower(out[i].Entry.N) < strings.ToLower(out[j].Entry.N)
	})
	return out, nil
}

// originalComponent returns component i of a token path (0 = the token)
// in its original case.
func originalComponent(tp string, i int) string {
	tok, comps, _ := snapshot.SplitTokenPath(tp)
	if i == 0 {
		return tok
	}
	return comps[i-1]
}

// listBelow lists target inside root r (rc are r's components).
func listBelow(ctx context.Context, tr TreeReader, r snapshot.RootRef, rc, target []string, path string) ([]Item, error) {
	id := r.Tree
	for depth, name := range target[len(rc):] {
		if depth > maxDepth {
			return nil, ErrTooDeep
		}
		next := ""
		err := entries(ctx, tr, id, func(e snapshot.Entry) error {
			if next == "" && e.T == snapshot.TypeDir && e.Sub != nil && strings.ToLower(e.N) == name {
				next = *e.Sub
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if next == "" {
			return nil, ErrNotFound
		}
		id = next
	}
	var out []Item
	err := entries(ctx, tr, id, func(e snapshot.Entry) error {
		if e.N != "" && !strings.ContainsAny(e.N, "/\\") {
			out = append(out, Item{Path: path + "/" + e.N, Entry: e})
		}
		return nil
	})
	return out, err
}

// Lookup returns the entry at token path p.
func Lookup(ctx context.Context, tr TreeReader, roots []snapshot.RootRef, p string) (Item, error) {
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return Item{}, ErrNotFound
	}
	items, err := ListDir(ctx, tr, roots, p[:i])
	if err != nil {
		return Item{}, err
	}
	name := strings.ToLower(p[i+1:])
	for _, it := range items {
		if strings.ToLower(it.Entry.N) == name {
			return it, nil
		}
	}
	return Item{}, ErrNotFound
}

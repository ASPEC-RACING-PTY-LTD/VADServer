package catalog

import (
	"context"
	"errors"
	"strconv"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// SearchQuery is a search request of api-v1 §5.4. Type is "" or a catalog
// type; MinSize/MaxSize are -1 when unset.
type SearchQuery struct {
	Q       string
	Type    string
	MinSize int64
	MaxSize int64
	AfterID int64
	Limit   int
}

// Page is one page of catalog entries; NextCursor is "" on the last page.
type Page struct {
	Items      []wire.CatalogEntry
	NextCursor string
}

func (s *Service) entries(uk *userKeys, sn *store.Snapshot, rows []store.CatalogFile, limit int) (Page, error) {
	p := Page{Items: make([]wire.CatalogEntry, 0, len(rows))}
	for i := range rows {
		f := &rows[i]
		path, err := uk.openField(sn.ID, "path", f.PathCT)
		if err != nil {
			return Page{}, err
		}
		name, err := uk.openField(sn.ID, "name", f.NameCT)
		if err != nil {
			return Page{}, err
		}
		p.Items = append(p.Items, wire.CatalogEntry{Path: path, Name: name, Type: f.Type, Size: f.Size, ModifiedAt: f.MTime})
	}
	if len(rows) == limit && limit > 0 {
		p.NextCursor = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	return p, nil
}

// keysForBrowse returns the user's catalog keys; a user without a DEK has
// nothing ingested, which callers see as an empty catalog.
func (s *Service) keysForBrowse(ctx context.Context, userID int64) (*userKeys, error) {
	return s.userKeysFor(ctx, userID, false)
}

// Tree lists the children of path ("" = the snapshot's backed-up roots) of
// an ingested snapshot, in tree order after the cursor id.
func (s *Service) Tree(ctx context.Context, sn *store.Snapshot, path string, afterID int64, limit int) (Page, error) {
	uk, err := s.keysForBrowse(ctx, sn.UserID)
	if errors.Is(err, ErrNoCatalogKey) {
		if path != "" {
			return Page{}, ErrPathNotFound
		}
		return Page{Items: []wire.CatalogEntry{}}, nil
	}
	if err != nil {
		return Page{}, err
	}
	defer uk.wipe()
	bi := uk.pathBI(path)
	if path != "" {
		f, err := s.st.CatalogFileByPath(ctx, sn.ID, bi)
		if errors.Is(err, store.ErrNotFound) {
			return Page{}, ErrPathNotFound
		}
		if err != nil {
			return Page{}, err
		}
		if f.Type != wire.CatalogEntryDir {
			return Page{}, ErrNotDirectory
		}
	}
	rows, err := s.st.CatalogChildren(ctx, sn.ID, bi, afterID, limit)
	if err != nil {
		return Page{}, err
	}
	return s.entries(uk, sn, rows, limit)
}

// Search returns the entries of an ingested snapshot whose name has every
// word of q as a whole word or as a prefix (blind-indexed; no substring
// search).
func (s *Service) Search(ctx context.Context, sn *store.Snapshot, q SearchQuery) (Page, error) {
	uk, err := s.keysForBrowse(ctx, sn.UserID)
	if errors.Is(err, ErrNoCatalogKey) {
		return Page{Items: []wire.CatalogEntry{}}, nil
	}
	if err != nil {
		return Page{}, err
	}
	defer uk.wipe()
	rows, err := s.st.SearchCatalog(ctx, store.CatalogSearchFilter{SnapshotID: sn.ID, Tokens: uk.queryTokens(q.Q), Type: q.Type,
		MinSize: q.MinSize, MaxSize: q.MaxSize, AfterID: q.AfterID, Limit: q.Limit})
	if err != nil {
		return Page{}, err
	}
	return s.entries(uk, sn, rows, q.Limit)
}

// HasQueryWords reports whether q contains at least one searchable word.
func HasQueryWords(q string) bool { return len(splitWords(q)) > 0 }

// WalkAll calls fn for every entry of an ingested snapshot in tree order
// (parents before children). A user without a catalog key has no entries.
func (s *Service) WalkAll(ctx context.Context, sn *store.Snapshot, fn func(wire.CatalogEntry) error) error {
	uk, err := s.keysForBrowse(ctx, sn.UserID)
	if errors.Is(err, ErrNoCatalogKey) {
		return nil
	}
	if err != nil {
		return err
	}
	defer uk.wipe()
	var after int64
	for {
		rows, err := s.st.CatalogFilesAfter(ctx, sn.ID, after, 1000)
		if err != nil {
			return err
		}
		page, err := s.entries(uk, sn, rows, 0)
		if err != nil {
			return err
		}
		for _, e := range page.Items {
			if err := fn(e); err != nil {
				return err
			}
		}
		if len(rows) < 1000 {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

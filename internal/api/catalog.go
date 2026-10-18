// Owner: SRV-C (P1-16). Scope: api-v1 §5.4 snapshot tree and search over
// the encrypted catalog built by internal/catalog, and the hook that queues
// catalog ingestion after a snapshot commit (§5.2 step 3).
//
//   - GET /snapshots/{id}/tree?path=&cursor=&limit= lists the children of
//     path ("" = the backed-up roots) in tree order; the cursor is the last
//     row id. A path that is not in the catalog is 404 path_not_found; a file
//     path is 422 validation.
//   - GET /snapshots/{id}/search?q=&type=&min_size=&max_size=&cursor=&limit=
//     matches every word of q against whole name words and their prefixes
//     (at least 2 characters; blind index, no substring search). type is
//     file, dir or symlink.
//   - Both answer for the snapshot owner or an admin session, 404
//     snapshot_not_found for anyone else, 409 not_browsable until ingestion
//     is done, and 409 escrow_unavailable without server key custody.

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/catalog"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	maxCatalogPath  = 32 << 10
	maxCatalogQuery = 256
)

var catalogServices sync.Map // *Server -> *catalog.Service

// Catalog returns the server's catalog service, creating it on first use.
// Ingestion only runs while RunCatalog is running.
func (s *Server) Catalog() *catalog.Service {
	if v, ok := catalogServices.Load(s); ok {
		return v.(*catalog.Service)
	}
	svc := catalog.New(catalog.Config{Store: s.st, Keys: s.Keys,
		Objects: func(ctx context.Context, rp *store.Repository) (*backup.Store, error) { return s.repoObjects(ctx, rp) }})
	v, _ := catalogServices.LoadOrStore(s, svc)
	return v.(*catalog.Service)
}

// RunCatalog runs the catalog ingestion worker until ctx ends.
func (s *Server) RunCatalog(ctx context.Context) { s.Catalog().Run(ctx) }

// queueCatalogIngest is called after a new snapshot commit.
func (s *Server) queueCatalogIngest(snapshotID int64) { s.Catalog().Enqueue(snapshotID) }

func errNotBrowsable() *apiError {
	return errf(http.StatusConflict, "not_browsable", "The snapshot catalog is not available yet")
}

// browsableSnapshot loads the snapshot for its owner or an admin and checks
// that its catalog is ingested.
func (s *Server) browsableSnapshot(r *http.Request) (*store.Snapshot, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	p := principal(r)
	sn, err := s.st.GetSnapshot(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sn.UserID != p.User.ID && !p.User.IsAdmin()) {
		return nil, errSnapshotNotFound()
	}
	if err != nil {
		return nil, err
	}
	if sn.IngestState != wire.SnapshotIngestDone {
		return nil, errNotBrowsable()
	}
	return sn, nil
}

func catalogErr(err error) error {
	switch {
	case errors.Is(err, catalog.ErrPathNotFound):
		return errf(404, "path_not_found", "Path not found in the snapshot")
	case errors.Is(err, catalog.ErrNotDirectory):
		return repoValidation(map[string]string{"path": "is not a directory"})
	}
	return keysErr(err)
}

func writeCatalogPage(w http.ResponseWriter, p catalog.Page) {
	writeJSON(w, 200, wire.List[wire.CatalogEntry]{Items: p.Items, NextCursor: p.NextCursor})
}

func (s *Server) handleSnapshotTree(w http.ResponseWriter, r *http.Request) {
	sn, err := s.browsableSnapshot(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	after, limit, err := listParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	path := r.URL.Query().Get("path")
	if len(path) > maxCatalogPath || !utf8.ValidString(path) {
		writeErr(w, r, repoValidation(map[string]string{"path": "invalid path"}))
		return
	}
	page, err := s.Catalog().Tree(r.Context(), sn, path, after, limit)
	if err != nil {
		writeErr(w, r, catalogErr(err))
		return
	}
	writeCatalogPage(w, page)
}

func (s *Server) handleSnapshotSearch(w http.ResponseWriter, r *http.Request) {
	sn, err := s.browsableSnapshot(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	after, limit, err := listParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v := r.URL.Query()
	q := catalog.SearchQuery{Q: v.Get("q"), Type: v.Get("type"), MinSize: -1, MaxSize: -1, AfterID: after, Limit: limit}
	fields := map[string]string{}
	if len(q.Q) > maxCatalogQuery || !utf8.ValidString(q.Q) || !catalog.HasQueryWords(q.Q) {
		fields["q"] = "must contain at least one word (letters or digits), at most 256 bytes"
	}
	switch q.Type {
	case "", wire.CatalogEntryFile, wire.CatalogEntryDir, "symlink":
	default:
		fields["type"] = "must be file, dir or symlink"
	}
	for name, dst := range map[string]*int64{"min_size": &q.MinSize, "max_size": &q.MaxSize} {
		if s := v.Get(name); s != "" {
			n, perr := strconv.ParseInt(s, 10, 64)
			if perr != nil || n < 0 {
				fields[name] = "must be a non-negative integer"
				continue
			}
			*dst = n
		}
	}
	if len(fields) > 0 {
		writeErr(w, r, repoValidation(fields))
		return
	}
	page, err := s.Catalog().Search(r.Context(), sn, q)
	if err != nil {
		writeErr(w, r, catalogErr(err))
		return
	}
	writeCatalogPage(w, page)
}

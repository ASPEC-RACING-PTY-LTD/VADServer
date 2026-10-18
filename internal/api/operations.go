// Owner: SRV-0 (P0-05). Scope: api-v1 §1 long operations and plan/execute,
// §3 operations (sess; dev only for operations it created).

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// mountOperations registers the routes of this feature area.
func (s *Server) mountOperations(rt routes) {
	rt.User.Get("/operations/{id}", s.handleOperation)
}

// maxRunningOperations bounds operations executing at once; further ones
// stay queued until a slot frees up.
const maxRunningOperations = 4

// opRunner runs long operations in-process for the server's lifetime.
type opRunner struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}
}

func newOpRunner() *opRunner {
	ctx, cancel := context.WithCancel(context.Background())
	return &opRunner{ctx: ctx, cancel: cancel, sem: make(chan struct{}, maxRunningOperations)}
}

// OpFunc is the body of a long operation. It reports progress through p and
// returns the kind-specific result (marshalled into Operation.result; nil
// stores {}). ctx is cancelled when the server shuts down. Return an
// *apiError (errf) for a message shown to the user; other errors are logged
// and shown as a generic failure.
type OpFunc func(ctx context.Context, p *OpProgress) (result any, err error)

// OpProgress reports progress of a running operation.
type OpProgress struct {
	ID int64
	st *store.Store
}

// Set records progress (done/total units and bytes processed).
func (p *OpProgress) Set(done, total, bytes int64) {
	if err := p.st.UpdateOperationProgress(context.Background(), p.ID, done, total, bytes); err != nil {
		slog.Warn("operation progress update failed", "operation_id", p.ID, "err", err)
	}
}

// OpStart describes an operation to start. UserID nil marks a system
// operation; Params is stored as JSON for diagnostics.
type OpStart struct {
	Kind     string
	UserID   *int64
	DeviceID *int64
	Params   any
}

// StartOperation stores a queued operation and runs fn in the background.
// It returns the operation id at once.
func (s *Server) StartOperation(o OpStart, fn OpFunc) (int64, error) {
	params := "{}"
	if o.Params != nil {
		b, err := json.Marshal(o.Params)
		if err != nil {
			return 0, err
		}
		params = string(b)
	}
	if err := s.ops.ctx.Err(); err != nil {
		return 0, errf(503, "shutting_down", "The server is shutting down")
	}
	op := &store.Operation{Kind: o.Kind, UserID: o.UserID, DeviceID: o.DeviceID, Params: params}
	if err := s.st.CreateOperation(s.ops.ctx, op); err != nil {
		return 0, err
	}
	s.ops.wg.Add(1)
	go s.runOperation(op.ID, op.Kind, fn)
	return op.ID, nil
}

func (s *Server) runOperation(id int64, kind string, fn OpFunc) {
	defer s.ops.wg.Done()
	ctx := s.ops.ctx
	bg := context.WithoutCancel(ctx) // final state is recorded even during shutdown
	select {
	case s.ops.sem <- struct{}{}:
		defer func() { <-s.ops.sem }()
	case <-ctx.Done():
		_ = s.st.FinishOperation(bg, id, wire.OperationStateCancelled, "", "cancelled by server shutdown")
		return
	}
	if err := s.st.StartOperation(bg, id); err != nil {
		slog.Error("operation start failed", "operation_id", id, "kind", kind, "err", err)
		return
	}
	res, err := func() (res any, err error) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("operation panicked", "operation_id", id, "kind", kind, "panic", v)
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		return fn(ctx, &OpProgress{ID: id, st: s.st})
	}()
	state, result, msg := wire.OperationStateSucceeded, "", ""
	switch {
	case err != nil && errors.Is(err, context.Canceled) && ctx.Err() != nil:
		state, msg = wire.OperationStateCancelled, "cancelled by server shutdown"
	case err != nil:
		state = wire.OperationStateFailed
		var ae *apiError
		if errors.As(err, &ae) {
			msg = ae.Message
		} else {
			slog.Error("operation failed", "operation_id", id, "kind", kind, "err", err)
			msg = "Internal server error"
		}
	case res != nil:
		b, merr := json.Marshal(res)
		if merr != nil {
			state, msg = wire.OperationStateFailed, "Internal server error"
			slog.Error("operation result not encodable", "operation_id", id, "kind", kind, "err", merr)
		} else {
			result = string(b)
		}
	}
	if err := s.st.FinishOperation(bg, id, state, result, msg); err != nil {
		slog.Error("operation finish failed", "operation_id", id, "kind", kind, "err", err)
	}
}

// startRequestOperation starts an operation owned by the caller (user and,
// for signed requests, device).
func (s *Server) startRequestOperation(r *http.Request, kind string, params any, fn OpFunc) (int64, error) {
	p := principal(r)
	if p == nil || p.User == nil {
		return 0, errf(401, "unauthenticated", "Sign in required")
	}
	uid := p.User.ID
	return s.StartOperation(OpStart{Kind: kind, UserID: &uid, DeviceID: p.deviceID(), Params: params}, fn)
}

// acceptOperation starts an operation for the caller and replies
// 202 {"operation_id"} (api-v1 §1 long operations).
func (s *Server) acceptOperation(w http.ResponseWriter, r *http.Request, kind string, params any, fn OpFunc) {
	id, err := s.startRequestOperation(r, kind, params, fn)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, wire.OperationAccepted{OperationID: id})
}

// StopOperations cancels running operations and waits for them to record
// their final state, or until ctx ends. Call it before closing the store.
func (s *Server) StopOperations(ctx context.Context) {
	s.ops.cancel()
	done := make(chan struct{})
	go func() { s.ops.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("operations still running at shutdown")
	}
}

func operationView(o *store.Operation) wire.Operation {
	return wire.Operation{
		ID: o.ID, Kind: o.Kind, State: o.State,
		Progress:  wire.OperationProgress{Done: o.Done, Total: o.Total, Bytes: o.Bytes},
		Result:    json.RawMessage(o.Result),
		Error:     o.Error,
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

// canViewOperation: a device sees only operations it created; a session
// sees its user's operations, and an admin also sees system operations.
func canViewOperation(p *Principal, o *store.Operation) bool {
	if p == nil || p.User == nil {
		return false
	}
	if p.Device != nil {
		return o.DeviceID != nil && *o.DeviceID == p.Device.ID && o.UserID != nil && *o.UserID == p.User.ID
	}
	if o.UserID == nil {
		return p.User.IsAdmin()
	}
	return *o.UserID == p.User.ID
}

func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	o, err := s.st.Operation(r.Context(), id)
	if err == nil && !canViewOperation(principal(r), o) {
		err = store.ErrNotFound
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, operationView(o))
}

// --- plan/execute (api-v1 §1) ---

// errPlanStale is the 409 plan_stale reply.
func errPlanStale() *apiError {
	return errf(409, "plan_stale", "The plan has expired or no longer matches the current state; create a new plan")
}

// createPlan stores plan for the session user and returns the …/plan reply.
// ttl 0 uses store.DefaultPlanTTL.
func (s *Server) createPlan(r *http.Request, kind, targetType string, targetID *int64, plan any, ttl time.Duration) (*wire.PlanResponse, error) {
	p := principal(r)
	if p == nil || p.User == nil {
		return nil, errf(401, "unauthenticated", "Sign in required")
	}
	token, sp, err := s.st.CreatePlan(r.Context(), store.NewPlan{UserID: p.User.ID, Kind: kind, TargetType: targetType, TargetID: targetID, Plan: plan, TTL: ttl})
	if err != nil {
		return nil, err
	}
	return &wire.PlanResponse{Plan: json.RawMessage(sp.Plan), PlanToken: token, PlanDigest: sp.Digest, ExpiresAt: sp.ExpiresAt}, nil
}

// executePlan consumes a plan token of kind for the session user. current
// recomputes the plan from current state (nil skips the check); a changed
// digest, an expired, used or foreign token all return 409 plan_stale.
func (s *Server) executePlan(r *http.Request, kind, token string, current func(*store.Plan) (any, error)) (*store.Plan, error) {
	p := principal(r)
	if p == nil || p.User == nil {
		return nil, errf(401, "unauthenticated", "Sign in required")
	}
	sp, err := s.st.ExecutePlan(r.Context(), p.User.ID, kind, token, current)
	if errors.Is(err, store.ErrPlanStale) {
		return nil, errPlanStale()
	}
	return sp, err
}

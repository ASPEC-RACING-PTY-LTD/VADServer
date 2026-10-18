package automation

// Op is a generic operation produced by an action. Kinds added after v1
// express their effect as ops instead of dedicated Outcome fields, so the
// desktop and the server can apply (or queue for approval) every action the
// same way.
type Op struct {
	// Kind is the operation id, e.g. "backup.run" (usually the action kind).
	Kind string `json:"kind"`
	// NodeID is the action node that emitted the op.
	NodeID string `json:"node_id,omitempty"`
	// Target identifies what the op acts on (a job id, a folder, ...). Ops
	// with the same Kind and Target are merged by MergeOps.
	Target string `json:"target,omitempty"`
	// Params are the op arguments.
	Params map[string]any `json:"params,omitempty"`
	// Approval is OpApprovalPending when the op must wait for a human
	// approval before it runs; empty otherwise.
	Approval string `json:"approval,omitempty"`
}

// OpApprovalPending marks an op that waits for approval (api-v1 §8).
const OpApprovalPending = "pending"

// EmitOp appends an op to the outcome. The engine fills NodeID and Approval
// for ops emitted by an action handler.
func (o *Outcome) EmitOp(op Op) {
	op.Params = cloneParams(op.Params)
	o.Ops = append(o.Ops, op)
}

func opKey(op Op) string { return op.Kind + "\x00" + op.Target }

// MergeOps merges b into a and returns a new slice; neither input is
// modified. Ops with the same Kind and Target are combined: params merge with
// MergeParams (b overrides a), NodeID is taken from b, and a pending approval
// is never dropped. Order is first appearance, so the result is deterministic.
func MergeOps(a, b []Op) []Op {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]Op, 0, len(a)+len(b))
	idx := map[string]int{}
	add := func(op Op) {
		k := opKey(op)
		i, seen := idx[k]
		if !seen {
			op.Params = cloneParams(op.Params)
			idx[k] = len(out)
			out = append(out, op)
			return
		}
		cur := &out[i]
		cur.Params = MergeParams(cur.Params, op.Params)
		if op.NodeID != "" {
			cur.NodeID = op.NodeID
		}
		if op.Approval == OpApprovalPending {
			cur.Approval = OpApprovalPending
		}
	}
	for _, op := range a {
		add(op)
	}
	for _, op := range b {
		add(op)
	}
	return out
}

// MergeParams returns defaults overlaid with overrides. Nested objects
// (map[string]any) are merged recursively; any other override value,
// including a list, replaces the default. An override set to nil removes the
// key. The result shares no maps or slices with the inputs. It returns nil
// only when both inputs are empty.
func MergeParams(defaults, overrides map[string]any) map[string]any {
	if len(defaults) == 0 && len(overrides) == 0 {
		return nil
	}
	out := cloneParams(defaults)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range overrides {
		if v == nil {
			delete(out, k)
			continue
		}
		om, ok1 := out[k].(map[string]any)
		vm, ok2 := v.(map[string]any)
		if ok1 && ok2 {
			if m := MergeParams(om, vm); m != nil {
				out[k] = m
			} else {
				out[k] = map[string]any{}
			}
			continue
		}
		out[k] = cloneValue(v)
	}
	return out
}

// NodeParams returns a node's params with the kind's declared defaults
// (ParamSpec.Default) filled in for missing keys. spec may be nil.
func NodeParams(spec *KindSpec, params map[string]any) map[string]any {
	var defaults map[string]any
	if spec != nil {
		for _, ps := range spec.Params {
			if ps.Default == nil {
				continue
			}
			if defaults == nil {
				defaults = map[string]any{}
			}
			defaults[ps.Name] = ps.Default
		}
	}
	return MergeParams(defaults, params)
}

func cloneParams(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneParams(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneValue(x)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	}
	return v
}

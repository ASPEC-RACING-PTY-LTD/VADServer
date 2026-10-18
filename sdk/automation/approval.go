package automation

import (
	"fmt"
	"sync"
)

// ApprovalLevel says whether a kind's effects need a human approval before
// they run (api-v1 §8).
type ApprovalLevel string

const (
	ApprovalNone     ApprovalLevel = "none"
	ApprovalOptional ApprovalLevel = "optional"
	ApprovalRequired ApprovalLevel = "required"
)

var approvalRank = map[ApprovalLevel]int{"": 0, ApprovalNone: 0, ApprovalOptional: 1, ApprovalRequired: 2}

// ApprovalPolicy is the approval metadata of a kind. It is catalogue data,
// never document data: a document cannot lower it.
type ApprovalPolicy struct {
	// Destructive marks kinds that delete or overwrite data. Destructive
	// kinds always require approval.
	Destructive bool          `json:"destructive,omitempty"`
	Approval    ApprovalLevel `json:"approval,omitempty"`
}

// Required reports whether the policy gates execution on an approval.
func (p ApprovalPolicy) Required() bool {
	return p.Destructive || p.Approval == ApprovalRequired
}

var (
	approvalMu sync.RWMutex
	approvals  = map[string]ApprovalPolicy{}
)

// SetApprovalPolicy sets the approval metadata of a kind of DefaultRegistry.
// Policies only ever tighten: a second call cannot clear Destructive or lower
// the level. All v1 built-in kinds have no policy (not destructive, none).
func SetApprovalPolicy(nodeType, kind string, p ApprovalPolicy) error {
	if DefaultRegistry.Lookup(nodeType, kind) == nil {
		return fmt.Errorf("automation: approval policy for unknown %s %q", nodeType, kind)
	}
	if _, ok := approvalRank[p.Approval]; !ok {
		return fmt.Errorf("automation: approval policy for %s %q: unknown level %q", nodeType, kind, p.Approval)
	}
	if p.Destructive {
		p.Approval = ApprovalRequired
	}
	approvalMu.Lock()
	defer approvalMu.Unlock()
	key := nodeType + "/" + kind
	cur := approvals[key]
	cur.Destructive = cur.Destructive || p.Destructive
	if approvalRank[p.Approval] > approvalRank[cur.Approval] {
		cur.Approval = p.Approval
	}
	approvals[key] = cur
	return nil
}

// MustSetApprovalPolicy is SetApprovalPolicy that panics on error.
func MustSetApprovalPolicy(nodeType, kind string, p ApprovalPolicy) {
	if err := SetApprovalPolicy(nodeType, kind, p); err != nil {
		panic(err)
	}
}

// ApprovalPolicy returns the kind's effective approval metadata: the shared
// policy table tightened by the spec's own Destructive/Approval fields. Kinds
// without a policy are not destructive and need no approval.
func (k KindSpec) ApprovalPolicy() ApprovalPolicy {
	return k.policy()
}

func approvalPolicy(nodeType, kind string) ApprovalPolicy {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	p := approvals[nodeType+"/"+kind]
	if p.Approval == "" {
		p.Approval = ApprovalNone
	}
	return p
}

// RequiresApproval lists, in document order, the ids of the nodes whose kind
// or node-level override requires an approval before it runs. Nil means nothing is gated.
func RequiresApproval(def *Automation) []string {
	if def == nil {
		return nil
	}
	var out []string
	for i := range def.Nodes {
		n := &def.Nodes[i]
		if NodeApproval(n).Required() {
			out = append(out, n.ID)
		}
	}
	return out
}

// markApproval stamps the ops an action node just emitted with the node id
// and, when the kind or the node override is approval-gated, a pending
// approval.
func markApproval(ops []Op, n *Node) {
	if len(ops) == 0 {
		return
	}
	gated := NodeApproval(n).Required()
	for i := range ops {
		if ops[i].NodeID == "" {
			ops[i].NodeID = n.ID
		}
		if gated {
			ops[i].Approval = OpApprovalPending
		}
	}
}

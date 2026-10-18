// Hand-written mirror of the wire Approval (sdk/wire/approvals.go, api-v1 §8 rev 1.0 + 1.1).

export type ApprovalKind =
  | 'restore'
  | 'prune'
  | 'gc'
  | 'unprotect_snapshot'
  | 'device_task'
  | 'automation_op'
  | 'ai_proposal'
  | 'escrow_change'
  | 'two_way_enable'

export type ApprovalSubjectType = 'restore' | 'prune_plan' | 'snapshot' | 'device_task' | 'automation'

/** `consumed` means the approved action was executed. */
export type ApprovalState = 'pending' | 'approved' | 'rejected' | 'expired' | 'consumed'

export type ApprovalRisk = 'low' | 'medium' | 'high'

export type ApprovalRequester = 'user' | 'automation' | 'ai' | 'device'

export interface Approval {
  id: number
  kind: ApprovalKind | (string & {})
  subject_type: ApprovalSubjectType | (string & {})
  subject_id: number
  /** SHA-256 hex of the canonical plan JSON. */
  plan_digest: string
  summary: string
  risk: ApprovalRisk | (string & {})
  requested_by: ApprovalRequester | (string & {})
  requester_ref: string
  state: ApprovalState | (string & {})
  /** Unix ms. */
  expires_at: number
  /** User id; null until decided. */
  decided_by: number | null
  /** Unix ms; null until decided. */
  decided_at: number | null
  reason: string
  /** Not in the rev 1.1 wire type. Rendered with PlanPreview when a server ever includes it. */
  plan?: unknown
}

/** Reply of approve/reject: the Approval plus the resuming operation (null when none). */
export type ApprovalDecided = Approval & { operation_id: number | null }

/** POST /approvals/{id}/approve. `confirm_text` is required for risk high and must equal the subject name shown in the summary. */
export interface ApprovalDecisionRequest {
  confirm_text?: string
}

/** POST /approvals/{id}/reject. */
export interface ApprovalRejectRequest {
  reason: string
}

export const APPROVAL_KINDS: readonly ApprovalKind[] = ['restore', 'prune', 'gc', 'unprotect_snapshot', 'device_task', 'automation_op', 'ai_proposal', 'escrow_change', 'two_way_enable']
export const APPROVAL_STATES: readonly ApprovalState[] = ['pending', 'approved', 'rejected', 'expired', 'consumed']

export const kindLabels: Record<ApprovalKind, string> = {
  restore: 'Restore',
  prune: 'Prune',
  gc: 'Garbage collection',
  unprotect_snapshot: 'Unprotect snapshot',
  device_task: 'Device task',
  automation_op: 'Automation',
  ai_proposal: 'AI proposal',
  escrow_change: 'Escrow change',
  two_way_enable: 'Enable two-way sync',
}

export const stateLabels: Record<ApprovalState, string> = {
  pending: 'Pending',
  approved: 'Approved',
  rejected: 'Rejected',
  expired: 'Expired',
  consumed: 'Executed',
}

export function kindLabel(k: string): string {
  return kindLabels[k as ApprovalKind] ?? k
}

export function stateLabel(s: string): string {
  return stateLabels[s as ApprovalState] ?? s
}

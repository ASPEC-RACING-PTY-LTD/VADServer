import { ApiError, post, withQuery, type Page } from '../../lib/api'
import { createKeys, queryKeys, useApiInfiniteList, useApiMutation, useApiQuery } from '../../lib/query'
import type { Approval, ApprovalDecided, ApprovalDecisionRequest } from './types'

export const approvalKeys = createKeys('approvals')

export type ApprovalListFilter = { state?: string }

/** `GET /approvals?state=` as a cursor list (newest first). */
export function useApprovals(filter: ApprovalListFilter = {}) {
  return useApiInfiniteList<Approval>(approvalKeys.list(filter), '/approvals', { state: filter.state, limit: 100 })
}

export function useApproval(id: number | null | undefined) {
  return useApiQuery<Approval>(approvalKeys.detail(id ?? 0), `/approvals/${id}`, { enabled: !!id })
}

/** First page of pending approvals, for the dashboard widget. */
export function usePendingApprovals(limit = 5) {
  return useApiQuery<Page<Approval>>(approvalKeys.sub('pending', limit), withQuery('/approvals', { state: 'pending', limit }), { refetchInterval: 30_000 })
}

const invalidate = [approvalKeys.all, queryKeys.summary]

export function useApproveApproval() {
  return useApiMutation<ApprovalDecided, { id: number; confirmText?: string }>(
    ({ id, confirmText }) => {
      const body: ApprovalDecisionRequest = confirmText ? { confirm_text: confirmText } : {}
      return post<ApprovalDecided>(`/approvals/${id}/approve`, body)
    },
    { invalidate },
  )
}

export function useRejectApproval() {
  return useApiMutation<ApprovalDecided, { id: number; reason: string }>(({ id, reason }) => post<ApprovalDecided>(`/approvals/${id}/reject`, { reason }), { invalidate })
}

/** User-facing text for approve/reject failures (api-v1 §8, P1-17 error codes). */
export function decisionErrorMessage(e: unknown): string {
  if (!(e instanceof ApiError)) return e instanceof Error ? e.message : String(e)
  switch (e.code) {
    case 'plan_stale':
      return 'The plan changed after this approval was requested, so it has expired. The requester must re-plan and request approval again.'
    case 'approval_not_pending': {
      const state = (e.details as { state?: string } | undefined)?.state
      return state ? `This approval has already been decided (it is now ${state}). The list has been refreshed.` : 'This approval is no longer pending. The list has been refreshed.'
    }
    case 'not_superuser':
      return 'Only a superuser can decide this approval.'
    case 'forbidden':
      return 'You are not allowed to decide this approval.'
    case 'validation': {
      const f = e.fieldErrors
      if (f.confirm_text) return 'The confirmation text does not match. Type the name shown in the summary exactly.'
      if (f.reason) return `Reason: ${f.reason}`
      return e.message
    }
    default:
      return e.message
  }
}

/** Errors after which the approval's state changed server-side, so cached data is out of date. */
export function isStateChangingError(e: unknown): boolean {
  return e instanceof ApiError && (e.code === 'plan_stale' || e.code === 'approval_not_pending')
}

import { useMemo, useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'
import { Badge, Button, EmptyState, Field, Modal, PageHeader, Tabs, useUI } from '../../components/ui'
import { DataTable, type Column } from '../../components/data/DataTable'
import { FilterBar, type FilterValues } from '../../components/data/FilterBar'
import { infiniteTableProps } from '../../components/data/infinite'
import { PlanPreview } from '../../components/PlanPreview'
import { OperationProgress } from '../../components/JobProgress'
import { formatDate, timeAgo } from '../../lib/format'
import { approvalKeys, decisionErrorMessage, isStateChangingError, useApproval, useApprovals, useApproveApproval, useRejectApproval } from './api'
import { APPROVAL_KINDS, APPROVAL_STATES, kindLabel, stateLabel, type Approval } from './types'

type View = 'pending' | 'recent' | 'all'

export function riskTone(risk: string): 'green' | 'amber' | 'red' | 'slate' {
  return risk === 'high' ? 'red' : risk === 'medium' ? 'amber' : risk === 'low' ? 'green' : 'slate'
}

export function stateTone(state: string): 'amber' | 'green' | 'red' | 'slate' | 'blue' {
  switch (state) {
    case 'pending':
      return 'amber'
    case 'approved':
      return 'blue'
    case 'consumed':
      return 'green'
    case 'rejected':
      return 'red'
    default:
      return 'slate'
  }
}

const columns: Column<Approval>[] = [
  {
    id: 'summary',
    header: 'Summary',
    cell: (a) => (
      <div className="min-w-0">
        <div className="truncate font-medium text-slate-900 dark:text-slate-100">{a.summary || `${kindLabel(a.kind)} #${a.subject_id}`}</div>
        <div className="text-xs text-slate-500 dark:text-slate-400">
          {a.subject_type} #{a.subject_id}
        </div>
      </div>
    ),
    sortValue: (a) => a.summary,
  },
  { id: 'kind', header: 'Kind', cell: (a) => kindLabel(a.kind), sortValue: (a) => kindLabel(a.kind) },
  { id: 'risk', header: 'Risk', cell: (a) => <Badge tone={riskTone(a.risk)}>{a.risk}</Badge>, sortValue: (a) => ['low', 'medium', 'high'].indexOf(a.risk) },
  { id: 'requested_by', header: 'Requested by', cell: (a) => (a.requester_ref ? `${a.requested_by} (${a.requester_ref})` : a.requested_by), sortValue: (a) => a.requested_by },
  { id: 'state', header: 'Status', cell: (a) => <Badge tone={stateTone(a.state)}>{stateLabel(a.state)}</Badge>, sortValue: (a) => a.state },
  {
    id: 'when',
    header: 'When',
    cell: (a) => (a.state === 'pending' ? <span title={formatDate(a.expires_at)}>expires {timeAgo(a.expires_at)}</span> : <span title={formatDate(a.decided_at)}>{a.decided_at ? timeAgo(a.decided_at) : '-'}</span>),
    sortValue: (a) => (a.state === 'pending' ? a.expires_at : (a.decided_at ?? 0)),
  },
]

/** Approvals inbox (api-v1 §8): pending, recent and all approvals with a detail dialog to approve or reject. */
export default function ApprovalsPage() {
  const { id: idParam } = useParams()
  const navigate = useNavigate()
  const [view, setView] = useState<View>('pending')
  const [filters, setFilters] = useState<FilterValues>({})
  const [search, setSearch] = useState('')

  const q = useApprovals(view === 'pending' ? { state: 'pending' } : {})

  const rows = useMemo(() => {
    const states = filters.state ?? []
    const kinds = filters.kind ?? []
    const term = search.trim().toLowerCase()
    return q.items.filter(
      (a) =>
        (view !== 'recent' || a.state !== 'pending') &&
        (states.length === 0 || states.includes(a.state)) &&
        (kinds.length === 0 || kinds.includes(a.kind)) &&
        (!term || a.summary.toLowerCase().includes(term) || a.requester_ref.toLowerCase().includes(term)),
    )
  }, [q.items, filters, search, view])

  const stateOptions = APPROVAL_STATES.filter((s) => (view === 'pending' ? false : view === 'recent' ? s !== 'pending' : true)).map((s) => ({ value: s, label: stateLabel(s) }))
  const kindOptions = APPROVAL_KINDS.map((k) => ({ value: k, label: kindLabel(k) }))

  const selectedId = idParam && /^\d+$/.test(idParam) ? Number(idParam) : null
  const selectedRow = selectedId ? q.items.find((a) => a.id === selectedId) : undefined

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Approvals" description="Destructive or sensitive actions wait here until you approve or reject them." icon={<ShieldCheck className="size-5" />} />
      <Tabs<View>
        value={view}
        onChange={(v) => {
          setView(v)
          setFilters((f) => ({ ...f, state: [] }))
        }}
        tabs={[
          { id: 'pending', label: 'Pending' },
          { id: 'recent', label: 'Recent' },
          { id: 'all', label: 'All' },
        ]}
      />
      <FilterBar
        search={search}
        onSearchChange={setSearch}
        searchPlaceholder="Search summaries…"
        filters={[...(stateOptions.length ? [{ id: 'state', label: 'Status', options: stateOptions, multiple: true }] : []), { id: 'kind', label: 'Kind', options: kindOptions, multiple: true }]}
        values={filters}
        onValuesChange={setFilters}
      />
      <DataTable
        {...infiniteTableProps(q)}
        rows={rows}
        columns={columns}
        rowKey={(a) => a.id}
        label="Approvals"
        onRowClick={(a) => navigate(`/approvals/${a.id}`)}
        empty={<EmptyState icon={<ShieldCheck className="size-6" />} title={view === 'pending' ? 'Nothing waiting for approval' : 'No approvals'} />}
      />
      {selectedId && <ApprovalDialog id={selectedId} initial={selectedRow} onClose={() => navigate('/approvals')} />}
    </div>
  )
}

function ApprovalDialog({ id, initial, onClose }: { id: number; initial?: Approval; onClose: () => void }) {
  const detail = useApproval(id)
  const a = detail.data ?? initial
  return (
    <Modal open onClose={onClose} title={a ? a.summary || `${kindLabel(a.kind)} #${a.subject_id}` : `Approval #${id}`} size="lg">
      {a ? <ApprovalDetail approval={a} onClose={onClose} /> : detail.error ? <p role="alert">{decisionErrorMessage(detail.error)}</p> : <p className="text-sm text-slate-500">Loading…</p>}
    </Modal>
  )
}

export function ApprovalDetail({ approval: a, onClose }: { approval: Approval; onClose: () => void }) {
  const { toast } = useUI()
  const qc = useQueryClient()
  const approve = useApproveApproval()
  const reject = useRejectApproval()
  const [confirmText, setConfirmText] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [operationId, setOperationId] = useState<number | null>(null)

  const pending = a.state === 'pending'
  const needsText = a.risk === 'high'
  const busy = approve.isPending || reject.isPending

  const fail = (e: unknown) => {
    setError(decisionErrorMessage(e))
    if (isStateChangingError(e)) void qc.invalidateQueries({ queryKey: approvalKeys.all })
  }

  async function doApprove() {
    if (needsText && !confirmText.trim()) throw new Error('Type the name shown in the summary to confirm.')
    setError(null)
    const res = await approve.mutateAsync({ id: a.id, confirmText: needsText ? confirmText.trim() : undefined })
    toast('success', 'Approved', res.operation_id ? 'The action is now running.' : undefined)
    if (res.operation_id) setOperationId(res.operation_id)
    else onClose()
  }

  async function doReject() {
    setError(null)
    try {
      await reject.mutateAsync({ id: a.id, reason: reason.trim() })
      toast('success', 'Rejected')
      onClose()
    } catch (e) {
      fail(e)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <Item label="Kind">{kindLabel(a.kind)}</Item>
        <Item label="Subject">
          {a.subject_type} #{a.subject_id}
        </Item>
        <Item label="Risk">
          <Badge tone={riskTone(a.risk)}>{a.risk}</Badge>
        </Item>
        <Item label="Status">
          <Badge tone={stateTone(a.state)}>{stateLabel(a.state)}</Badge>
        </Item>
        <Item label="Requested by">{a.requester_ref ? `${a.requested_by} (${a.requester_ref})` : a.requested_by}</Item>
        <Item label={pending ? 'Expires' : 'Decided'}>{pending ? formatDate(a.expires_at) : a.decided_at ? formatDate(a.decided_at) : '-'}</Item>
        {a.reason && <Item label="Reason">{a.reason}</Item>}
        <Item label="Plan digest">
          <span className="font-mono text-xs break-all">{a.plan_digest || '-'}</span>
        </Item>
      </dl>

      {operationId !== null && <OperationProgress operationId={operationId} title="Running approved action" />}

      {pending && operationId === null && (
        <>
          {needsText && (
            <Field label="Confirmation" hint="This is a high-risk action. Type the name shown in the summary (for example the device or repository name) exactly.">
              <input className="input" aria-label="Confirmation text" value={confirmText} onChange={(e) => setConfirmText(e.target.value)} autoComplete="off" spellCheck={false} disabled={busy} />
            </Field>
          )}
          {error && (
            <p role="alert" className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
              {error}
            </p>
          )}
          {rejecting ? (
            <div className="flex flex-col gap-3">
              <Field label="Reason for rejecting (optional)">
                <textarea className="input" aria-label="Reason" rows={3} maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} disabled={busy} />
              </Field>
              <div className="flex justify-end gap-2">
                <Button variant="ghost" onClick={() => setRejecting(false)} disabled={busy}>
                  Back
                </Button>
                <Button variant="danger" loading={reject.isPending} onClick={() => void doReject()}>
                  Reject
                </Button>
              </div>
            </div>
          ) : a.plan !== undefined && a.plan !== null ? (
            <PlanPreview
              response={{ plan: a.plan, plan_token: `approval-${a.id}`, plan_digest: a.plan_digest, expires_at: a.expires_at }}
              onConfirm={async () => {
                try {
                  await doApprove()
                } catch (e) {
                  fail(e)
                }
              }}
              onCancel={() => setRejecting(true)}
              cancelLabel="Reject…"
              confirmLabel="Approve"
              danger={a.risk === 'high'}
            />
          ) : (
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setRejecting(true)} disabled={busy}>
                Reject…
              </Button>
              <Button
                variant={a.risk === 'high' ? 'danger' : 'primary'}
                loading={approve.isPending}
                disabled={busy || (needsText && !confirmText.trim())}
                onClick={() => void doApprove().catch(fail)}
              >
                Approve
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  )
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col">
      <dt className="text-xs text-slate-500 dark:text-slate-400">{label}</dt>
      <dd className="text-slate-900 dark:text-slate-100">{children}</dd>
    </div>
  )
}

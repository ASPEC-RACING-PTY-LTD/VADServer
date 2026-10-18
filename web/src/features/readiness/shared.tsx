import { CheckCircle2, CircleHelp, OctagonAlert, TriangleAlert } from 'lucide-react'
import { Badge, cx } from '../../components/ui'
import { statusLabel, statusTone } from './types'

export function StatusIcon({ status, className = 'size-5' }: { status: string; className?: string }) {
  if (status === 'ok') return <CheckCircle2 className={cx(className, 'text-emerald-500')} aria-hidden />
  if (status === 'warning') return <TriangleAlert className={cx(className, 'text-amber-500')} aria-hidden />
  if (status === 'critical') return <OctagonAlert className={cx(className, 'text-rose-500')} aria-hidden />
  return <CircleHelp className={cx(className, 'text-slate-400')} aria-hidden />
}

export function StatusBadge({ status }: { status: string }) {
  return <Badge tone={statusTone(status)}>{statusLabel(status)}</Badge>
}

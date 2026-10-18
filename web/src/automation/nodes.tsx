import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { AlertCircle, AlertTriangle, Bell, Calendar, Clock, File, FilePen, FilePlus2, FileX2, Filter, Folder, FolderInput, FolderPlus, GitBranch, Hash, Pencil, Play, RefreshCw, Ruler, SkipForward, Tag, Upload, Variable, XCircle, Zap } from 'lucide-react'
import type { FlowNode } from './model'
import { cx } from '../components/ui'

export const kindIcons: Record<string, React.ComponentType<{ className?: string }>> = {
  'file.created': FilePlus2,
  'file.modified': FilePen,
  'file.renamed': Pencil,
  'file.deleted': FileX2,
  'folder.changed': Folder,
  'schedule.interval': Clock,
  'sync.completed': RefreshCw,
  'sync.failed': XCircle,
  'file.size_threshold': Ruler,
  'file.extension_matched': Hash,
  'name.contains': Filter,
  'name.matches': Filter,
  'extension.in': Hash,
  'size.compare': Ruler,
  'path.matches': Folder,
  'file.age': Clock,
  'date.compare': Calendar,
  'variable.compare': Variable,
  group: GitBranch,
  'sync.file': Upload,
  'sync.skip': SkipForward,
  'dest.set_folder': FolderInput,
  'dest.rename': Pencil,
  'dest.create_folder': FolderPlus,
  'tag.apply': Tag,
  'sync.start_task': Play,
  'notify.send': Bell,
}

export const typeStyle = {
  trigger: { ring: 'ring-brand-500', bar: 'bg-brand-500', chip: 'bg-brand-100 text-brand-700 dark:bg-brand-500/20 dark:text-brand-300', label: 'Trigger', icon: Zap },
  condition: { ring: 'ring-amber-500', bar: 'bg-amber-500', chip: 'bg-amber-100 text-amber-800 dark:bg-amber-500/20 dark:text-amber-300', label: 'Condition', icon: GitBranch },
  action: { ring: 'ring-emerald-500', bar: 'bg-emerald-500', chip: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-500/20 dark:text-emerald-300', label: 'Action', icon: Play },
}

function Body({ data, selected, type }: { data: FlowNode['data']; selected?: boolean; type: 'trigger' | 'condition' | 'action' }) {
  const st = typeStyle[type]
  const Icon = kindIcons[data.node.kind] ?? File
  return (
    <div className={cx('w-64 overflow-hidden rounded-xl border bg-white shadow-md transition dark:bg-slate-900', data.errors.length ? 'border-rose-400 ring-2 ring-rose-400/40' : 'border-slate-200 dark:border-slate-700', selected && !data.errors.length && `ring-2 ${st.ring}`)}>
      <div className={cx('h-1', st.bar)} />
      <div className="p-3">
        <div className="flex items-center gap-2">
          <span className={cx('flex size-7 shrink-0 items-center justify-center rounded-lg', st.chip)}>
            <Icon className="size-4" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[10px] font-semibold tracking-wider text-slate-400 uppercase">{st.label}</div>
            <div className="truncate text-sm font-semibold text-slate-800 dark:text-slate-100">{data.node.label || data.spec?.title || data.node.kind}</div>
          </div>
          {data.errors.length > 0 ? <AlertCircle className="size-4 shrink-0 text-rose-500" /> : data.warnings.length > 0 ? <AlertTriangle className="size-4 shrink-0 text-amber-500" /> : null}
        </div>
        {data.summary && <div className="mt-2 line-clamp-2 rounded-md bg-slate-50 px-2 py-1 font-mono text-[11px] break-all text-slate-600 dark:bg-slate-800 dark:text-slate-300">{data.summary}</div>}
        {data.node.label && data.spec && <div className="mt-1 truncate text-[10px] text-slate-400">{data.spec.title}</div>}
      </div>
    </div>
  )
}

const handleCls = '!size-3 !border-2'

export const TriggerNode = memo(({ data, selected }: NodeProps<FlowNode>) => (
  <>
    <Body data={data} selected={selected} type="trigger" />
    <Handle type="source" position={Position.Right} id="next" className={handleCls} />
  </>
))

export const ActionNode = memo(({ data, selected }: NodeProps<FlowNode>) => (
  <>
    <Handle type="target" position={Position.Left} id="in" className={handleCls} />
    <Body data={data} selected={selected} type="action" />
    <Handle type="source" position={Position.Right} id="next" className={handleCls} />
  </>
))

export const ConditionNode = memo(({ data, selected }: NodeProps<FlowNode>) => (
  <>
    <Handle type="target" position={Position.Left} id="in" className={handleCls} />
    <Body data={data} selected={selected} type="condition" />
    <Handle type="source" position={Position.Right} id="true" style={{ top: '35%', background: '#10b981' }} className={handleCls} />
    <Handle type="source" position={Position.Right} id="false" style={{ top: '72%', background: '#f43f5e' }} className={handleCls} />
    <span className="pointer-events-none absolute top-[35%] -right-9 -translate-y-1/2 text-[10px] font-bold text-emerald-600">YES</span>
    <span className="pointer-events-none absolute top-[72%] -right-8 -translate-y-1/2 text-[10px] font-bold text-rose-500">NO</span>
  </>
))

export const nodeTypes = { trigger: TriggerNode, condition: ConditionNode, action: ActionNode }

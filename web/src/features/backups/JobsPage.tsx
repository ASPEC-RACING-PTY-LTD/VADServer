import { Link, useNavigate } from 'react-router-dom'
import { HardDriveDownload, Plus } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, PageLoader } from '../../components/ui'
import { DataTable, type Column } from '../../components/data/DataTable'
import { infiniteTableProps } from '../../components/data/infinite'
import { describeSchedule } from '../../components/ScheduleEditor'
import { useDevices, useJobs } from './api'
import { kindLabel, modeLabels, type DeviceLite, type Job, type JobMode } from './types'
import { JobActions, JobStateBadge, suspensionReason, useJobActions } from './shared'

/** Backup jobs (api-v1 §6), one table per device (Sync > Backups & Restore). */
export function BackupJobsPanel() {
  const navigate = useNavigate()
  const devices = useDevices()
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 font-semibold">
            <HardDriveDownload className="size-4" /> Backup jobs
          </h2>
          <p className="text-sm text-slate-500">Optional. Runs start on schedule or when you choose Run now.</p>
        </div>
        <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => navigate('/backups/jobs/new')}>
          New job
        </Button>
      </div>
      {devices.error ? (
        <ErrorState error={devices.error} onRetry={() => void devices.refetch()} />
      ) : devices.isPending ? (
        <PageLoader />
      ) : devices.data.length === 0 ? (
        <EmptyState icon={<HardDriveDownload className="size-6" />} title="No devices" description="Enrol a device first, then create backup jobs for it." />
      ) : (
        devices.data.map((d) => <DeviceJobs key={d.id} device={d} />)
      )}
    </div>
  )
}

function DeviceJobs({ device }: { device: DeviceLite }) {
  const navigate = useNavigate()
  const q = useJobs(device.id)
  const actions = useJobActions()
  const columns: Column<Job>[] = [
    {
      id: 'name',
      header: 'Job',
      cell: (j) => (
        <div className="min-w-0">
          <div className="truncate font-medium text-slate-900 dark:text-slate-100">{j.name}</div>
          <div className="text-xs text-slate-500 dark:text-slate-400">
            {kindLabel(j.kind)} · {modeLabels[j.mode as JobMode] ?? j.mode}
          </div>
        </div>
      ),
      sortValue: (j) => j.name,
    },
    { id: 'schedule', header: 'Schedule', cell: (j) => describeSchedule(j.schedule) },
    {
      id: 'state',
      header: 'State',
      cell: (j) => {
        const why = suspensionReason(j)
        return (
          <div className="flex flex-col gap-1">
            <JobStateBadge job={j} />
            {why && <span className="text-xs text-slate-500 dark:text-slate-400">{why}</span>}
          </div>
        )
      },
      sortValue: (j) => j.state,
    },
    { id: 'actions', header: <span className="sr-only">Actions</span>, cell: (j) => <JobActions job={j} actions={actions} />, align: 'right' },
  ]
  return (
    <section className="flex flex-col gap-3" aria-label={`Jobs on ${device.name}`}>
      <div className="flex items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 font-semibold text-slate-900 dark:text-white">
          {device.name}
          {device.install_mode && <Badge tone="slate">{device.install_mode}</Badge>}
        </h2>
        <Link to={`/backups/jobs/new?device=${device.id}`} className="text-sm font-medium text-brand-600 hover:underline dark:text-brand-400">
          Add job
        </Link>
      </div>
      <DataTable
        {...infiniteTableProps(q)}
        columns={columns}
        rowKey={(j) => j.id}
        label={`Backup jobs on ${device.name}`}
        onRowClick={(j) => navigate(`/backups/jobs/${j.id}`)}
        empty={
          <EmptyState
            icon={<HardDriveDownload className="size-6" />}
            title="No backup jobs"
            action={
              <Button size="sm" onClick={() => navigate(`/backups/jobs/new?device=${device.id}`)}>
                Create a job
              </Button>
            }
          />
        }
      />
    </section>
  )
}

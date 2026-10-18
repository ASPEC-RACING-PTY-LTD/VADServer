import { Badge } from '../../components/ui'
import { timeAgo } from '../../lib/format'
import { modeLabel, moduleLabel, type DeviceModeFields } from './types'

/**
 * Install mode plus module badges for one device (api-v1 §6A). Modules come
 * only from the device's report; nothing is inferred from the mode.
 */
export function ModeBadges({ device }: { device: DeviceModeFields }) {
  if (!device.install_mode && !device.modules_summary) return null
  const sum = device.modules_summary
  const reported = !!sum && sum.updated_at > 0
  return (
    <div className="mt-1 flex flex-wrap items-center gap-1" data-testid="mode-badges">
      <Badge tone={device.install_mode === 'advanced' ? 'violet' : 'blue'}>{modeLabel(device.install_mode)} mode</Badge>
      {!reported ? (
        <Badge tone="slate">Modules not reported</Badge>
      ) : (
        <>
          {sum.enabled.map((m) => (
            <Badge key={'e-' + m} tone="green">
              {moduleLabel(m)}
            </Badge>
          ))}
          {sum.failed.map((m) => (
            <Badge key={'f-' + m} tone="red">
              {moduleLabel(m)} failed
            </Badge>
          ))}
          <span className="text-[11px] text-slate-400">reported {timeAgo(sum.updated_at)}</span>
        </>
      )}
    </div>
  )
}

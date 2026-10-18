/*
 * Human-readable descriptions for activity-log and transfer events.
 * Shared by the Activity page, the admin console and anything else that lists events.
 */

const actionLabels: Record<string, string> = {
  'file.uploaded': 'Uploaded',
  'file.downloaded': 'Downloaded',
  'file.trashed': 'Moved to trash',
  'file.purged': 'Permanently deleted',
  'trash.emptied': 'Emptied trash',
  'file.restored': 'Restored',
  'file.renamed': 'Renamed',
  'file.moved': 'Moved',
  'folder.created': 'Created folder',
  'auth.login': 'Signed in',
  'auth.logout': 'Signed out',
  'auth.failed': 'Failed sign-in attempt',
  'auth.denied': 'Sign-in denied',
  'device.requested': 'Device requested access',
  'device.approved': 'Approved device',
  'device.rejected': 'Rejected device',
  'device.revoked': 'Revoked device',
  'automation.saved': 'Saved automation',
  'automation.deleted': 'Deleted automation',
  'account.password_changed': 'Changed password',
  'account.setup': 'Set up VaultDock',
}

const transferLabels: Record<string, string> = {
  completed: 'Uploaded',
  deduplicated: 'Uploaded (deduplicated)',
  unchanged: 'Unchanged',
  active: 'Uploading',
  failed: 'Upload failed',
  cancelled: 'Upload cancelled',
  skipped: 'Skipped',
}

/** An activity-log row (`action`) or a transfer row (`status`); other fields are ignored. */
export type DescribableEvent = { action?: string | null; status?: string | null }

/**
 * Short human label for an event: the activity action ("Moved to trash") or, for a transfer
 * without an action, its status ("Upload failed"). Unknown values are returned as-is.
 */
export function describe(e: DescribableEvent): string {
  if (e.action) return actionLabels[e.action] ?? e.action
  if (e.status) return transferLabels[e.status] ?? e.status
  return ''
}

/** Where a transfer came from: "user · device", the device, "Browser" for web uploads, or the raw source. */
export function transferSource(t: { username?: string; device_name?: string; source?: string }, withUser = false): string {
  return `${withUser && t.username ? `${t.username} · ` : ''}${t.device_name || (t.source === 'web' ? 'Browser' : (t.source ?? ''))}`
}

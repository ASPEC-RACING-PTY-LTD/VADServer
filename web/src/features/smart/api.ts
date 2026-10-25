import { useQuery } from '@tanstack/react-query'
import { del, get, patch, post, put, withQuery } from '../../lib/api'
import { createKeys } from '../../lib/query'

export const smartKeys = createKeys('smart')

// --- filters ---

export type Rect = { x: number; y: number; w: number; h: number }

export type FilterNode = {
  op?: 'and' | 'or' | 'then' | 'without'
  window?: number
  children?: FilterNode[]
  kind?: string
  mode?: string
  labels?: string[]
  label?: string
  text?: string
  terms?: string[]
  match?: 'any' | 'all'
  region?: Rect
  name?: string
  side?: string
  db?: number
  count?: number
  min_score?: number
  min_duration?: number
}

export type Rule = { id: string; name: string; category: string; disabled?: boolean; when: FilterNode }

export type SegmentContext = {
  threshold: number
  min_event: number
  max_segment: number
  pre: number
  post: number
  merge: boolean
  merge_gap: number
  trim_silence: boolean
  snap_speech: boolean
  dedupe: number
  max_suggestions: number
}

export type Filter = { rules: Rule[]; context: SegmentContext }

export type Component = { rule: string; kind: string; summary: string; detectors: string[]; available: boolean; fallback?: boolean; missing?: string[]; reliability: string }

export type KindSpec = {
  kind: string
  title: string
  category: string
  description: string
  reliability: string
  modes?: string[]
  labels?: string[]
  needs_text?: boolean
  primary: string[]
  fallback?: string[]
  models?: string[]
  default_score: number
}

export type DetectorInfo = { id: string; title: string; description: string; model?: string; available: boolean; reason?: string }

export type Catalog = { kinds: KindSpec[]; sounds: string[]; detectors: DetectorInfo[]; context: SegmentContext; regions: Record<string, Rect> }

export type Preset = { id: string; name: string; description: string; builtin: boolean; filter: Filter; source?: string; created_at?: number; updated_at?: number; components: Component[] }

export type Plan = { description: string; interpreter: string; filter: Filter; components: Component[]; unsupported: string[]; notes: string[]; status: 'supported' | 'partial' | 'unavailable' | 'unsupported' }

// --- status ---

export type ModelStatus = {
  title: string
  capability: string
  license: string
  source: string
  installed: boolean
  loaded: boolean
  bytes: number
  estimate: number
  downloading: boolean
  progress: number
  error?: string
  optional?: boolean
}

export type Job = { item_id: number; queue_id: number; name: string; kind: 'analysis' | 'render'; stage: string; progress: number; started_at: number }

export type SmartStatus = {
  enabled: boolean
  ffmpeg: boolean
  worker: { configured: boolean; reachable: boolean; error?: string; version?: string; device?: string; models?: Record<string, ModelStatus>; busy?: number; rss?: number; cpu_percent?: number; threads?: number; gpu?: string }
  detectors: DetectorInfo[]
  running: Job[]
  resources?: Record<string, number | string>
  settings?: SmartSettings
}

export type SmartSettings = {
  enabled: boolean
  worker_url: string
  analysis_concurrency: number
  render_concurrency: number
  ffmpeg_threads: number
  low_priority: boolean
  hwaccel: string
  frame_rate: number
  frame_width: number
  ocr_every: number
  chunk_seconds: number
  language: string
  max_minutes: number
  use_language_model: boolean
}

// --- queues ---

export type QueueState = 'active' | 'paused' | 'cancelled' | 'completed' | 'archived'

export type Counters = {
  generated_count: number
  generated_bytes: number
  deleted_count: number
  deleted_bytes: number
  freed_bytes: number
  analysis_ms: number
  render_ms: number
  failures: number
}

export type Tally = {
  items: number
  item_states: Record<string, number>
  original_bytes: number
  analysed: number
  events: number
  segments: Record<string, number>
  outputs: Record<string, number>
  output_bytes: number
  processing_ms: number
}

export type OutputSettings = { quality: 'auto' | 'copy' | 'high' | 'balanced' | 'small'; destination: 'beside' | 'subfolder' | 'folder'; subfolder?: string; folder_id?: number }

export type Queue = {
  id: number
  name: string
  description: string
  state: QueueState
  auto_approve: boolean
  filter_rev: number
  counters: Counters
  created_at: number
  updated_at: number
  completed_at?: number
  archived_at?: number
  presets: string[]
  output: OutputSettings
  filter: Filter
  tally?: Tally
  components?: Component[]
  running?: Job[]
}

export type ItemState = 'pending' | 'analysing' | 'ready' | 'reviewed' | 'rendering' | 'done' | 'failed' | 'skipped' | 'cancelled' | 'gone'

export type Item = {
  id: number
  queue_id: number
  node_id: number
  sha256: string
  name: string
  path: string
  size: number
  duration: number
  state: ItemState
  stage: string
  progress: number
  error?: string
  attempts: number
  events: number
  eval_rev: number
  processing_ms: number
  position: number
  original_state: 'kept' | 'deleted' | 'trashed' | 'removed'
  deleted_at?: number
  deleted_bytes: number
  freed_bytes: number
  added_at: number
  analysed_at?: number
  reviewed_at?: number
  updated_at: number
  warnings: string[]
  detectors: string[]
  segments?: { suggested: number; approved: number; rejected: number }
}

export type Reason = { kind: string; detector: string; text: string; score: number; start: number; end: number }

export type Segment = {
  id: number
  item_id: number
  queue_id: number
  start: number
  end: number
  score: number
  state: 'suggested' | 'approved' | 'rejected'
  origin: 'auto' | 'edited' | 'manual' | 'split' | 'merged'
  category: string
  label: string
  name: string
  reasons: Reason[]
  eval_rev: number
  created_at: number
  updated_at: number
}

export type Output = { id: number; segment_id: number; item_id: number; render_key: string; state: 'pending' | 'rendering' | 'done' | 'failed'; node_id?: number; size: number; name: string; path: string; mode: string; error?: string; finished_at?: number }

export type TimelineEvent = { start: number; end: number; score: number; rule: string; rule_name: string; category: string; reasons: Reason[] }

export type Timeline = { events: TimelineEvent[]; loudness?: number[]; motion?: number[]; speech?: { start: number; end: number }[]; warnings: string[]; rate: number }

export type ItemDetail = { item: Item; segments: Segment[]; outputs: Output[]; timeline: Timeline | null; queue: { id: number; name: string; state: QueueState } | null; file_exists: boolean }

export type Utterance = { start: number; end: number; text: string; speaker: number; words?: { w: string; s: number; e: number }[] }

export type CleanupState = {
  item_id: number
  queue_id: number
  node_id: number
  name: string
  path: string
  size: number
  status: 'eligible' | 'no_segments' | 'blocked' | 'cleaned'
  reason?: string
  clips: number
  clip_bytes: number
  approved: number
}

export type CleanupResult = { item_id: number; name: string; status: 'deleted' | 'trashed' | 'skipped' | 'failed'; reason?: string; bytes: number; freed: number }

export type QueueStats = Counters & {
  queue_id: number
  name: string
  state: QueueState
  tally: Tally | null
  completion: number
  estimated_bytes: number
  estimated_count: number
  net_bytes: number
  created_at: number
  completed_at?: number
}

export type FileSmartState = { node_id: number; state: 'queued' | 'analysing' | 'review' | 'rendering' | 'processed' | 'failed' | ''; cleanup?: 'eligible' | 'no_segments'; clips: number; queues: number[]; output?: boolean }

export type Sources = { file_ids?: number[]; folder_ids?: number[]; recursive?: boolean }

// --- calls ---

export const smartApi = {
  status: (fresh = false) => get<SmartStatus>(`/smart/status${fresh ? '?fresh=1' : ''}`),
  catalog: () => get<Catalog>('/smart/catalog'),
  check: (filter: Filter) => post<{ filter: Filter; components: Component[] }>('/smart/check', { filter }),
  interpret: (description: string) => post<Plan>('/smart/interpret', { description }),
  presets: () => get<{ presets: Preset[] }>('/smart/presets'),
  savePreset: (id: string | null, p: { name: string; description: string; source?: string; filter: Filter }) => (id ? put<Preset>(`/smart/presets/${id}`, p) : post<Preset>('/smart/presets', p)),
  deletePreset: (id: string) => del(`/smart/presets/${id}`),

  queues: (state?: string) => get<{ queues: Queue[] }>(withQuery('/smart/queues', { state })),
  queue: (id: number) => get<Queue>(`/smart/queues/${id}`),
  createQueue: (q: { name: string; description?: string; preset_ids: string[]; filter?: Filter; context?: SegmentContext; output?: OutputSettings; auto_approve?: boolean; paused?: boolean } & Sources) => post<Queue>('/smart/queues', q),
  updateQueue: (id: number, q: Partial<{ name: string; description: string; output: OutputSettings; auto_approve: boolean }>) => patch<Queue>(`/smart/queues/${id}`, q),
  setQueueFilter: (id: number, f: { preset_ids: string[]; filter?: Filter; context?: SegmentContext }) => put<Queue>(`/smart/queues/${id}/filter`, f),
  queueAction: (id: number, action: 'pause' | 'resume' | 'cancel' | 'retry' | 'archive' | 'reopen' | 'duplicate') => post<Queue>(`/smart/queues/${id}/actions/${action}`),
  deleteQueue: (id: number) => del(`/smart/queues/${id}`),
  items: (id: number, state?: string) => get<{ items: Item[]; total: number }>(withQuery(`/smart/queues/${id}/items`, { state })),
  addItems: (id: number, s: Sources) => post<{ added: number; skipped: number; found: number }>(`/smart/queues/${id}/items`, s),
  removeItems: (id: number, itemIds: number[]) => post<{ removed: number }>(`/smart/queues/${id}/items/remove`, { item_ids: itemIds }),
  errors: (id: number) => get<{ errors: { id: number; item_id?: number; at: number; stage: string; message: string }[] }>(`/smart/queues/${id}/errors`),

  item: (id: number) => get<ItemDetail>(`/smart/items/${id}`),
  transcript: (id: number) => get<{ available: boolean; language?: string; speakers?: number; utterances: Utterance[] }>(`/smart/items/${id}/transcript`),
  itemAction: (id: number, action: 'review' | 'skip' | 'reopen' | 'retry', body?: { reject_rest?: boolean }) => post<ItemDetail>(`/smart/items/${id}/actions/${action}`, body ?? {}),
  nextReview: (p: { queue_id?: number; after?: number; dir?: 'next' | 'prev' }) => get<{ item: Item | null; remaining?: number }>(withQuery('/smart/review/next', p)),
  createSegment: (itemId: number, s: { start: number; end: number; name?: string; state?: string }) => post<Segment>(`/smart/items/${itemId}/segments`, s),
  updateSegment: (id: number, s: Partial<{ start: number; end: number; state: string; name: string }>) => patch<Segment>(`/smart/segments/${id}`, s),
  deleteSegment: (id: number) => del(`/smart/segments/${id}`),
  splitSegment: (id: number, at: number) => post<{ segments: Segment[] }>(`/smart/segments/${id}/split`, { at }),
  mergeSegments: (ids: number[]) => post<Segment>('/smart/segments/merge', { ids }),
  bulkSegments: (b: { ids?: number[]; item_ids?: number[]; queue_id?: number; categories?: string[]; min_score?: number; state: string; finish?: boolean }) => post<{ updated: number; reviewed: number }>('/smart/segments/bulk', b),

  search: (q: string, queueId?: number) => get<{ hits: { item_id: number; queue_id: number; name: string; path: string; start: number; end: number; speaker: number; text: string }[]; truncated: boolean }>(withQuery('/smart/search', { q, queue_id: queueId })),
  files: (ids: number[]) => post<{ files: FileSmartState[] }>('/smart/files', { ids }),
  folderFiles: (folderId: number, recursive: boolean, select?: string) =>
    get<{ files: { id: number; name: string; path: string; size: number; smart?: FileSmartState }[]; count: number; bytes: number }>(withQuery('/smart/files/folder', { folder_id: folderId, recursive: recursive ? 1 : 0, select })),

  cleanup: (p: { queue_id?: number; folder_id?: number; recursive?: boolean }) =>
    get<{ items: CleanupState[]; totals: Record<string, { count: number; bytes: number; clip_bytes: number }> }>(withQuery('/smart/cleanup', { queue_id: p.queue_id, folder_id: p.folder_id, recursive: p.recursive === false ? 0 : undefined })),
  runCleanup: (itemIds: number[], mode: 'delete' | 'trash', includeNoSegments: boolean) =>
    post<{ results: CleanupResult[]; removed: number; bytes: number; freed: number }>('/smart/cleanup', { item_ids: itemIds, mode, include_no_segments: includeNoSegments }),
  stats: (queueId?: number) => get<{ queues: QueueStats[]; total: QueueStats }>(withQuery('/smart/stats', { queue_id: queueId })),

  adminSettings: () => get<{ settings: SmartSettings; env_worker_url: string; token_set: boolean }>('/admin/smart/settings'),
  saveAdminSettings: (s: SmartSettings) => put<{ settings: SmartSettings }>('/admin/smart/settings', s),
  models: (b: { install?: string[]; remove?: string[] }) => post('/admin/smart/models', b),
}

// --- hooks ---

export const useSmartStatus = (refetchInterval?: number) => useQuery({ queryKey: smartKeys.sub('status'), queryFn: () => smartApi.status(), refetchInterval })
export const useCatalog = () => useQuery({ queryKey: smartKeys.sub('catalog'), queryFn: smartApi.catalog, staleTime: 60_000 })
export const usePresets = () => useQuery({ queryKey: smartKeys.sub('presets'), queryFn: smartApi.presets })
export const useQueues = (state?: string, refetchInterval?: number) => useQuery({ queryKey: smartKeys.list({ state }), queryFn: () => smartApi.queues(state), refetchInterval })
export const useQueue = (id: number) => useQuery({ queryKey: smartKeys.detail(id), queryFn: () => smartApi.queue(id), refetchInterval: 4000 })
export const useStats = (queueId?: number) => useQuery({ queryKey: smartKeys.sub('stats', queueId ?? 0), queryFn: () => smartApi.stats(queueId) })

// --- labels ---

export const categoryLabel: Record<string, string> = {
  emotion: 'Emotion & reactions',
  conversation: 'Conversation',
  gaming: 'Gaming',
  visual: 'Visual',
  audio: 'Audio',
  speech: 'Speech & keywords',
  custom: 'Custom',
}

export const categoryTone: Record<string, 'violet' | 'blue' | 'green' | 'amber' | 'red' | 'slate'> = {
  emotion: 'violet',
  conversation: 'blue',
  gaming: 'green',
  visual: 'amber',
  audio: 'red',
  speech: 'blue',
  custom: 'slate',
}

export const itemStateLabel: Record<ItemState, string> = {
  pending: 'Queued',
  analysing: 'Analysing',
  ready: 'Ready for review',
  reviewed: 'Waiting to render',
  rendering: 'Rendering',
  done: 'Done',
  failed: 'Failed',
  skipped: 'Skipped',
  cancelled: 'Cancelled',
  gone: 'File removed',
}

export const itemStateTone = (s: ItemState): 'slate' | 'green' | 'amber' | 'red' | 'blue' | 'violet' =>
  s === 'done' ? 'green' : s === 'failed' || s === 'gone' ? 'red' : s === 'ready' || s === 'skipped' ? 'amber' : s === 'analysing' || s === 'rendering' ? 'blue' : s === 'reviewed' ? 'violet' : 'slate'

export const queueStateTone = (s: QueueState): 'slate' | 'green' | 'amber' | 'red' | 'blue' => (s === 'active' ? 'blue' : s === 'completed' ? 'green' : s === 'paused' ? 'amber' : s === 'cancelled' ? 'red' : 'slate')

export function fmtTime(t: number): string {
  if (!isFinite(t)) return '-'
  const s = Math.max(0, t)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  const ss = sec.toFixed(1).padStart(4, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

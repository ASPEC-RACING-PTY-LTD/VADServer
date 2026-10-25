import { del, get, post, put } from '../../lib/api'
import type { Node } from '../../components/files/types'
import type { Clip, MediaInfo, Project, Quality } from './model'

export type MediaStatus = { available: boolean; version: string }

export type MediaItem = { id: number; state: 'ready' | 'pending' | 'failed' | 'unavailable'; duration?: number; width?: number; height?: number; thumbnail: boolean }

export type SourceState = 'ok' | 'changed' | 'trashed' | 'missing'

export type Draft = {
  id: number
  source_node_id: number
  source_sha256: string
  source_name: string
  name: string
  data?: Project
  created_at: number
  updated_at: number
  source_state: SourceState
  source_path?: string
}

export type ExportClipState = {
  number: number
  name: string
  state: 'pending' | 'running' | 'done' | 'failed' | 'cancelled'
  mode: Quality
  progress: number
  estimate: number
  size?: number
  node_id?: number
  path?: string
  error?: string
}

export type ExportJob = {
  id: number
  source_node_id: number
  state: 'queued' | 'running' | 'done' | 'failed' | 'cancelled' | 'interrupted'
  created_at: number
  finished_at?: number
  progress: {
    clips: ExportClipState[]
    current: number
    original: { state: 'kept' | 'trashed' | 'failed'; node_id: number; name: string; size: number; error?: string; expires_at?: number }
    folder: string
    quality: Quality
    erase_original: boolean
    error?: string
  }
}

export const thumbnailURL = (n: Pick<Node, 'id' | 'sha256'>) => `/api/v1/files/${n.id}/thumbnail${n.sha256 ? `?v=${n.sha256.slice(0, 16)}` : ''}`
export const frameURL = (id: number, t: number, h: number, v?: string) => `/api/v1/files/${id}/frame?t=${t.toFixed(2)}&h=${h}${v ? `&v=${v.slice(0, 16)}` : ''}`
export const editorURL = (id: number) => `/video-editor/${id}`
export const draftURL = (id: number) => `/video-editor/draft/${id}`

export const mediaStatus = () => get<MediaStatus>('/media/status')
export const mediaBatch = (ids: number[]) => get<{ items: MediaItem[] }>(`/files/media?ids=${ids.join(',')}`)
export const mediaInfo = (id: number) => get<{ node: Node; info: MediaInfo }>(`/files/${id}/media`)

export const listDrafts = (source?: number) => get<{ items: Draft[] }>(`/video/drafts${source ? `?source=${source}` : ''}`)
export const getDraft = (id: number) => get<Draft>(`/video/drafts/${id}`)
export const createDraft = (sourceId: number, name: string, data: Project) => post<Draft>('/video/drafts', { source_id: sourceId, name, data })
export const updateDraft = (id: number, name: string, data: Project) => put<Draft>(`/video/drafts/${id}`, { name, data })
export const deleteDraft = (id: number) => del(`/video/drafts/${id}`)

export type ExportRequest = {
  source_id: number
  source_sha256: string
  draft_id?: number
  folder_id: number
  quality: Quality
  erase_original: boolean
  clips: { number: number; name: string; clip: ClipSpec }[]
}

type ClipSpec = Pick<Clip, 'start' | 'end' | 'speed' | 'volume' | 'muted' | 'freezes' | 'crop'> & { preserve_pitch: boolean }

export function clipSpec(c: Clip): ClipSpec {
  return { start: c.start, end: c.end, speed: c.speed, preserve_pitch: c.preservePitch, volume: c.volume, muted: c.muted, freezes: c.freezes, crop: c.crop }
}

export const startExport = (r: ExportRequest) => post<ExportJob>('/video/exports', r)
export const getExport = (id: number) => get<ExportJob>(`/video/exports/${id}`)
export const cancelExport = (id: number) => post(`/video/exports/${id}/cancel`)
export const activeExports = () => get<{ items: ExportJob[] }>('/video/exports?active=1')

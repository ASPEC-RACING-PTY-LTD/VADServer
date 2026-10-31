import { del, get, patch, post, withQuery } from '../../lib/api'

export type Share = {
  id: number
  node_id: number
  token: string
  url: string
  name: string
  path?: string
  title: string
  description: string
  allow_download: boolean
  created_at: number
  expires_at?: number
  revoked_at?: number
  views: number
  last_viewed_at?: number
  state: 'active' | 'expired' | 'revoked' | 'unavailable'
}

export type PublicShare = {
  title: string
  description: string
  name: string
  size: number
  mime: string
  recorded_at: number
  shared_at: number
  expires_at?: number
  allow_download: boolean
  shared_by: string
  site: string
  duration?: number
  width?: number
  height?: number
}

export const EXPIRY = [
  { label: '1 hour', seconds: 3600 },
  { label: '1 day', seconds: 86400 },
  { label: '7 days', seconds: 7 * 86400 },
  { label: '30 days', seconds: 30 * 86400 },
  { label: 'Never', seconds: 0 },
] as const

export const shareApi = {
  list: (nodeId?: number) => get<{ shares: Share[] }>(withQuery('/shares', { node_id: nodeId })),
  create: (b: { node_id: number; title: string; description: string; expires_in: number; allow_download: boolean }) => post<Share>('/shares', b),
  update: (id: number, b: Partial<{ title: string; description: string; expires_in: number; allow_download: boolean }>) => patch<Share>(`/shares/${id}`, b),
  revoke: (id: number) => del(`/shares/${id}`),
  public: (token: string) => get<PublicShare>(`/public/shares/${encodeURIComponent(token)}`),
}

export const shareLink = (s: Pick<Share, 'url'>) => window.location.origin + s.url
export const publicVideoURL = (token: string, download = false) => `/api/v1/public/shares/${encodeURIComponent(token)}/video${download ? '?download=1' : ''}`
export const publicPosterURL = (token: string) => `/api/v1/public/shares/${encodeURIComponent(token)}/poster`

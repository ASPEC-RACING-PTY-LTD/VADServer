import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Calendar, Clock, Download, Film, User } from 'lucide-react'
import { ApiError } from '../../lib/api'
import { formatBytes, formatDate, formatDuration } from '../../lib/format'
import { publicPosterURL, publicVideoURL, shareApi } from './api'

/** The public page of a shared video (/s/<token>): no account needed. */
export default function PublicSharePage({ token }: { token: string }) {
  const q = useQuery({ queryKey: ['public-share', token], queryFn: () => shareApi.public(token), retry: false, refetchOnWindowFocus: false })
  const s = q.data
  useEffect(() => {
    if (s) document.title = `${s.title} · ${s.site || 'VaultDock'}`
  }, [s])

  return (
    <div className="min-h-screen bg-slate-950 text-slate-100">
      <header className="mx-auto flex max-w-6xl items-center gap-2 px-4 py-4 text-sm text-slate-400">
        <Film className="size-4 text-brand-400" />
        <span className="font-semibold text-slate-200">{s?.site || 'VaultDock'}</span>
      </header>
      <main className="mx-auto max-w-6xl px-4 pb-16">
        {q.isLoading ? (
          <div className="flex aspect-video items-center justify-center rounded-2xl bg-slate-900 text-slate-500">Loading…</div>
        ) : q.error || !s ? (
          <Unavailable error={q.error} />
        ) : (
          <>
            <div className="overflow-hidden rounded-2xl bg-black shadow-2xl ring-1 ring-white/10">
              <video src={publicVideoURL(token)} poster={publicPosterURL(token)} controls autoPlay playsInline preload="metadata" className="aspect-video w-full bg-black" />
            </div>
            <div className="mt-6 flex flex-wrap items-start gap-4">
              <div className="min-w-0 flex-1">
                <h1 className="text-2xl font-bold break-words text-white sm:text-3xl">{s.title}</h1>
                <div className="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-sm text-slate-400">
                  {s.shared_by && (
                    <span className="flex items-center gap-1.5">
                      <User className="size-4" /> {s.shared_by}
                    </span>
                  )}
                  <span className="flex items-center gap-1.5">
                    <Calendar className="size-4" /> {formatDate(s.recorded_at)}
                  </span>
                  {s.duration ? (
                    <span className="flex items-center gap-1.5">
                      <Clock className="size-4" /> {formatDuration(s.duration)}
                    </span>
                  ) : null}
                  {s.width ? (
                    <span>
                      {s.width}×{s.height}
                    </span>
                  ) : null}
                </div>
                {s.description && <p className="mt-4 max-w-3xl text-base whitespace-pre-wrap text-slate-300">{s.description}</p>}
              </div>
              {s.allow_download && (
                <a href={publicVideoURL(token, true)} className="inline-flex items-center gap-2 rounded-xl bg-brand-600 px-4 py-2.5 text-sm font-semibold text-white shadow hover:bg-brand-500">
                  <Download className="size-4" /> Download ({formatBytes(s.size)})
                </a>
              )}
            </div>
            {s.expires_at && <p className="mt-8 text-xs text-slate-500">This link works until {formatDate(s.expires_at)}.</p>}
          </>
        )}
      </main>
    </div>
  )
}

function Unavailable({ error }: { error: unknown }) {
  const expired = error instanceof ApiError && error.status === 410
  return (
    <div className="flex aspect-video flex-col items-center justify-center gap-2 rounded-2xl bg-slate-900 px-6 text-center">
      <Film className="size-10 text-slate-600" />
      <h1 className="text-xl font-semibold text-white">{expired ? 'This link has expired' : 'This video is not available'}</h1>
      <p className="text-sm text-slate-400">{expired ? 'Ask the person who shared it for a new link.' : 'The link may be wrong, or it was removed by the person who shared it.'}</p>
    </div>
  )
}

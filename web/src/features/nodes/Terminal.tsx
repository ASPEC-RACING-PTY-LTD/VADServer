import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { X } from 'lucide-react'
import { del, errorMessage, get, post } from '../../lib/api'
import { Button, Field, Modal } from '../../components/ui'

const toB64 = (bytes: Uint8Array) => {
  let s = ''
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i])
  return btoa(s)
}
const fromB64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0))

/**
 * A terminal on a storage node. The node's owner must have allowed it on the node; opening one asks
 * for the password again and is recorded in the audit log. Input and output travel over short HTTP
 * requests (output is long-polled).
 */
export function NodeTerminal({ nodeId, nodeName, onClose }: { nodeId: number; nodeName: string; onClose: () => void }) {
  const [session, setSession] = useState<string | null>(null)
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const open = async () => {
    setBusy(true)
    setError(null)
    try {
      const cols = Math.max(80, Math.floor((window.innerWidth - 80) / 8.4))
      const rows = Math.max(24, Math.floor((window.innerHeight - 160) / 17))
      const r = await post<{ session: string }>(`/admin/nodes/${nodeId}/terminal`, { password, cols, rows })
      setPassword('')
      setSession(r.session)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  if (!session) {
    return (
      <Modal
        open
        onClose={onClose}
        title={`Terminal on ${nodeName}`}
        description="Commands run on the node's machine. Opening a terminal is recorded in the audit log."
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" loading={busy} disabled={!password} onClick={open}>
              Open terminal
            </Button>
          </>
        }
      >
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (password) void open()
          }}
        >
          <Field label="Your password" error={error ?? undefined}>
            <input className="input" type="password" autoComplete="current-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
        </form>
      </Modal>
    )
  }
  return <TerminalWindow session={session} nodeName={nodeName} onClose={onClose} />
}

function TerminalWindow({ session, nodeName, onClose }: { session: string; nodeName: string; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null)
  const [closed, setClosed] = useState<string | null>(null)

  useEffect(() => {
    const term = new XTerm({ fontSize: 13, cursorBlink: true, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace', theme: { background: '#0b0d12' } })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(box.current!)
    fit.fit()
    term.focus()
    let active = true
    let pending: Uint8Array[] = []
    let timer: number | undefined
    let size = { cols: term.cols, rows: term.rows }
    let sizeChanged = true
    const enc = new TextEncoder()

    const flush = async () => {
      timer = undefined
      const parts = pending
      pending = []
      const len = parts.reduce((n, p) => n + p.length, 0)
      const all = new Uint8Array(len)
      let off = 0
      for (const p of parts) {
        all.set(p, off)
        off += p.length
      }
      const body: Record<string, unknown> = { data: toB64(all) }
      if (sizeChanged) {
        body.cols = size.cols
        body.rows = size.rows
        sizeChanged = false
      }
      try {
        await post(`/admin/terminal/${session}/input`, body)
      } catch (e) {
        term.write(`\r\n\x1b[31m${errorMessage(e)}\x1b[0m\r\n`)
      }
    }
    const queue = (b: Uint8Array) => {
      pending.push(b)
      if (timer === undefined) timer = window.setTimeout(flush, 15)
    }
    const dataSub = term.onData((d) => queue(enc.encode(d)))
    const binSub = term.onBinary((d) => queue(Uint8Array.from(d, (c) => c.charCodeAt(0) & 255)))
    const ro = new ResizeObserver(() => {
      fit.fit()
      if (term.cols !== size.cols || term.rows !== size.rows) {
        size = { cols: term.cols, rows: term.rows }
        sizeChanged = true
        queue(new Uint8Array())
      }
    })
    ro.observe(box.current!)
    queue(new Uint8Array()) // send the initial size

    void (async () => {
      let seq = 0
      while (active) {
        try {
          const r = await get<{ seq: number; data: string; closed: boolean; reason?: string }>(`/admin/terminal/${session}/output?after=${seq}`)
          if (!active) break
          if (r.data) term.write(fromB64(r.data))
          seq = r.seq
          if (r.closed) {
            setClosed(r.reason || 'The terminal was closed')
            break
          }
        } catch (e) {
          if (!active) break
          setClosed(errorMessage(e))
          break
        }
      }
    })()

    return () => {
      active = false
      dataSub.dispose()
      binSub.dispose()
      ro.disconnect()
      if (timer !== undefined) window.clearTimeout(timer)
      term.dispose()
      void del(`/admin/terminal/${session}`).catch(() => {})
    }
  }, [session])

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-black/70 p-4 backdrop-blur-sm" role="dialog" aria-label={`Terminal on ${nodeName}`}>
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-slate-700 bg-[#0b0d12] shadow-2xl">
        <div className="flex items-center gap-3 border-b border-slate-800 px-3 py-2 text-sm text-slate-300">
          <span className="font-medium">Terminal · {nodeName}</span>
          {closed && <span className="text-xs text-amber-400">Closed: {closed}</span>}
          <button type="button" className="ml-auto rounded p-1 text-slate-400 hover:bg-slate-800 hover:text-white" aria-label="Close terminal" onClick={onClose}>
            <X className="size-4" />
          </button>
        </div>
        <div ref={box} className="min-h-0 flex-1 p-2" />
      </div>
    </div>
  )
}

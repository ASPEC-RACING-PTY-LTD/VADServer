import { cloneElement, createContext, isValidElement, useCallback, useContext, useEffect, useId, useRef, useState, type ButtonHTMLAttributes, type KeyboardEvent as ReactKeyboardEvent, type ReactElement, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, CheckCircle2, Info, Loader2, X, XCircle } from 'lucide-react'

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(' ')
}

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success'
const variants: Record<Variant, string> = {
  primary: 'bg-brand-600 text-white hover:bg-brand-700 shadow-sm shadow-brand-600/20 disabled:hover:bg-brand-600',
  secondary: 'border border-slate-300 bg-white text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800',
  ghost: 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800',
  danger: 'bg-rose-600 text-white hover:bg-rose-700 shadow-sm',
  success: 'bg-emerald-600 text-white hover:bg-emerald-700 shadow-sm',
}

export function Button({ variant = 'secondary', size = 'md', loading, icon, children, className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: 'sm' | 'md' | 'lg'; loading?: boolean; icon?: ReactNode }) {
  const sizes = { sm: 'h-8 px-2.5 text-xs gap-1.5', md: 'h-9 px-3.5 text-sm gap-2', lg: 'h-11 px-5 text-sm gap-2' }
  return (
    <button
      {...rest}
      type={rest.type ?? 'button'}
      disabled={rest.disabled || loading}
      aria-busy={loading || undefined}
      className={cx('inline-flex shrink-0 items-center justify-center rounded-lg font-medium whitespace-nowrap transition disabled:cursor-not-allowed disabled:opacity-50', sizes[size], variants[variant], className)}
    >
      {loading ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : icon}
      {children}
    </button>
  )
}

export function IconButton({ label, children, className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button {...rest} type={rest.type ?? 'button'} aria-label={label} title={label} className={cx('inline-flex size-8 items-center justify-center rounded-lg text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 disabled:opacity-40 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-100', className)}>
      {children}
    </button>
  )
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 aria-hidden="true" className={cx('animate-spin text-brand-500', className ?? 'size-5')} />
}

export function PageLoader({ label = 'Loading' }: { label?: string }) {
  return (
    <div role="status" aria-live="polite" className="flex h-64 flex-col items-center justify-center gap-3 text-sm text-slate-500">
      <Spinner className="size-7" />
      {label}
    </div>
  )
}

export function Badge({ tone = 'slate', children, className }: { tone?: 'slate' | 'green' | 'amber' | 'red' | 'blue' | 'violet'; children: ReactNode; className?: string }) {
  const tones = {
    slate: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300',
    green: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-500/15 dark:text-emerald-300',
    amber: 'bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-300',
    red: 'bg-rose-100 text-rose-800 dark:bg-rose-500/15 dark:text-rose-300',
    blue: 'bg-sky-100 text-sky-800 dark:bg-sky-500/15 dark:text-sky-300',
    violet: 'bg-brand-100 text-brand-800 dark:bg-brand-500/15 dark:text-brand-300',
  }
  return <span className={cx('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap', tones[tone], className)}>{children}</span>
}

export function PageHeader({ title, description, actions, icon }: { title: ReactNode; description?: ReactNode; actions?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div className="flex items-start gap-3">
        {icon && <div className="mt-0.5 flex size-10 items-center justify-center rounded-xl bg-brand-100 text-brand-700 dark:bg-brand-500/15 dark:text-brand-300">{icon}</div>}
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-slate-900 dark:text-white">{title}</h1>
          {description && <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">{description}</p>}
        </div>
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

export function EmptyState({ icon, title, description, action }: { icon: ReactNode; title: string; description?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center px-6 py-16 text-center">
      <div className="mb-4 flex size-14 items-center justify-center rounded-2xl bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">{icon}</div>
      <h3 className="text-base font-semibold text-slate-800 dark:text-slate-100">{title}</h3>
      {description && <p className="mt-1 max-w-md text-sm text-slate-500 dark:text-slate-400">{description}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const msg = error instanceof Error ? error.message : String(error)
  return (
    <div role="alert" className="flex flex-col items-center gap-3 rounded-2xl border border-rose-200 bg-rose-50 px-6 py-10 text-center dark:border-rose-500/30 dark:bg-rose-500/10">
      <XCircle className="size-8 text-rose-500" aria-hidden="true" />
      <div className="text-sm font-medium text-rose-800 dark:text-rose-200">{msg}</div>
      {onRetry && (
        <Button size="sm" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  )
}

export function ProgressBar({ value, tone = 'brand', className, indeterminate }: { value: number; tone?: 'brand' | 'green' | 'amber' | 'red'; className?: string; indeterminate?: boolean }) {
  const tones = { brand: 'bg-brand-500', green: 'bg-emerald-500', amber: 'bg-amber-500', red: 'bg-rose-500' }
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={indeterminate ? undefined : Math.round(Math.max(0, Math.min(100, value)))}
      className={cx('h-2 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-800', className)}
    >
      {indeterminate ? <div className={cx('h-full w-1/3 rounded-full animate-indeterminate', tones[tone])} /> : <div className={cx('h-full rounded-full transition-all duration-300', tones[tone])} style={{ width: `${Math.max(0, Math.min(100, value))}%` }} />}
    </div>
  )
}

export function Toggle({ checked, onChange, label, description, disabled }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode; description?: ReactNode; disabled?: boolean }) {
  const id = useId()
  return (
    <label className={cx('flex items-start gap-3', disabled ? 'opacity-60' : 'cursor-pointer')}>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-labelledby={label ? `${id}-label` : undefined}
        aria-describedby={description ? `${id}-desc` : undefined}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cx('relative mt-0.5 inline-flex h-5 w-9 shrink-0 rounded-full transition', checked ? 'bg-brand-600' : 'bg-slate-300 dark:bg-slate-700')}
      >
        <span aria-hidden="true" className={cx('absolute top-0.5 size-4 rounded-full bg-white shadow transition', checked ? 'left-[18px]' : 'left-0.5')} />
      </button>
      {(label || description) && (
        <span>
          {label && <span id={`${id}-label`} className="block text-sm font-medium text-slate-800 dark:text-slate-100">{label}</span>}
          {description && <span id={`${id}-desc`} className="block text-xs text-slate-500 dark:text-slate-400">{description}</span>}
        </span>
      )}
    </label>
  )
}

const fieldControls = new Set(['input', 'select', 'textarea'])

// Field labels its control. When the only child is a native input/select/textarea it gets an id (unless it
// has one), aria-describedby for the hint/error and aria-invalid; any other child is wrapped as a labelled group.
export function Field({ label, hint, error, children }: { label: string; hint?: ReactNode; error?: string; children: ReactNode }) {
  const id = useId()
  const labelId = `${id}-label`
  const msgId = error || hint ? `${id}-msg` : undefined
  let control: ReactNode = children
  let controlId: string | undefined
  if (isValidElement(children) && typeof children.type === 'string' && fieldControls.has(children.type)) {
    const el = children as ReactElement<Record<string, unknown>>
    controlId = (el.props.id as string | undefined) ?? `${id}-control`
    const describedBy = [el.props['aria-describedby'] as string | undefined, msgId].filter(Boolean).join(' ') || undefined
    control = cloneElement(el, {
      id: controlId,
      'aria-describedby': describedBy,
      'aria-invalid': error ? true : (el.props['aria-invalid'] as boolean | undefined),
    })
  }
  return (
    <div role={controlId ? undefined : 'group'} aria-labelledby={controlId ? undefined : labelId} aria-describedby={controlId ? undefined : msgId}>
      <label id={labelId} htmlFor={controlId} className="label">
        {label}
      </label>
      {control}
      {error ? (
        <p id={msgId} role="alert" className="mt-1 text-xs text-rose-600">
          {error}
        </p>
      ) : hint ? (
        <p id={msgId} className="mt-1 text-xs text-slate-500 dark:text-slate-400">
          {hint}
        </p>
      ) : null}
    </div>
  )
}

export function Tabs<T extends string>({ value, onChange, tabs }: { value: T; onChange: (v: T) => void; tabs: { id: T; label: ReactNode; count?: number }[] }) {
  return (
    <div className="no-scrollbar flex gap-1 overflow-x-auto rounded-xl bg-slate-100 p-1 dark:bg-slate-800/60">
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          aria-pressed={value === t.id}
          onClick={() => onChange(t.id)}
          className={cx('flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium whitespace-nowrap transition', value === t.id ? 'bg-white text-slate-900 shadow-sm dark:bg-slate-900 dark:text-white' : 'text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200')}
        >
          {t.label}
          {t.count !== undefined && t.count > 0 && <span className="rounded-full bg-brand-600 px-1.5 text-[10px] font-semibold text-white">{t.count}</span>}
        </button>
      ))}
    </div>
  )
}

export function Stat({ label, value, sub, icon, tone = 'brand' }: { label: string; value: ReactNode; sub?: ReactNode; icon: ReactNode; tone?: 'brand' | 'green' | 'amber' | 'red' | 'sky' }) {
  const tones = {
    brand: 'bg-brand-100 text-brand-700 dark:bg-brand-500/15 dark:text-brand-300',
    green: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
    amber: 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
    red: 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300',
    sky: 'bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300',
  }
  return (
    <div className="card flex items-center gap-4 p-5">
      <div className={cx('flex size-11 shrink-0 items-center justify-center rounded-xl', tones[tone])}>{icon}</div>
      <div className="min-w-0">
        <div className="text-xs font-medium tracking-wide text-slate-500 uppercase dark:text-slate-400">{label}</div>
        <div className="truncate text-xl font-semibold text-slate-900 dark:text-white">{value}</div>
        {sub && <div className="truncate text-xs text-slate-500 dark:text-slate-400">{sub}</div>}
      </div>
    </div>
  )
}

// --- Modal ---

const focusableSelector = 'a[href], area[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), iframe, [contenteditable="true"], [tabindex]:not([tabindex="-1"])'

function focusables(root: HTMLElement) {
  return Array.from(root.querySelectorAll<HTMLElement>(focusableSelector)).filter((el) => el.getClientRects().length > 0 || el === document.activeElement)
}

// Open modals, innermost last. Only the top one reacts to Escape and Tab.
const modalStack: number[] = []
let modalSeq = 0

export function Modal({ open, onClose, title, children, footer, size = 'md', description }: { open: boolean; onClose: () => void; title: ReactNode; description?: ReactNode; children: ReactNode; footer?: ReactNode; size?: 'sm' | 'md' | 'lg' | 'xl' | 'full' }) {
  const id = useId()
  const dialogRef = useRef<HTMLDivElement>(null)
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  })
  useEffect(() => {
    if (!open) return
    const key = ++modalSeq
    modalStack.push(key)
    const isTop = () => modalStack[modalStack.length - 1] === key
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null
    // Children with autoFocus have already taken focus; otherwise focus the dialog itself.
    const dialog = dialogRef.current
    if (dialog && !dialog.contains(document.activeElement)) dialog.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || !isTop()) return
      e.preventDefault()
      onCloseRef.current()
    }
    window.addEventListener('keydown', onKey)
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      window.removeEventListener('keydown', onKey)
      const i = modalStack.indexOf(key)
      if (i >= 0) modalStack.splice(i, 1)
      document.body.style.overflow = prev
      if (returnTo && returnTo.isConnected) returnTo.focus()
    }
  }, [open])
  if (!open) return null
  const sizes = { sm: 'max-w-md', md: 'max-w-lg', lg: 'max-w-2xl', xl: 'max-w-4xl', full: 'max-w-6xl' }
  // Focus trap: keep Tab / Shift+Tab inside the dialog.
  const onDialogKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'Tab' || !dialogRef.current) return
    const els = focusables(dialogRef.current)
    if (els.length === 0) {
      e.preventDefault()
      dialogRef.current.focus()
      return
    }
    const first = els[0]
    const last = els[els.length - 1]
    const active = document.activeElement
    if (e.shiftKey && (active === first || active === dialogRef.current || !dialogRef.current.contains(active))) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && (active === last || !dialogRef.current.contains(active))) {
      e.preventDefault()
      first.focus()
    }
  }
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-end justify-center p-0 sm:items-center sm:p-6">
      <div className="absolute inset-0 bg-slate-950/50 backdrop-blur-[2px]" aria-hidden="true" onClick={onClose} />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-title`}
        aria-describedby={description ? `${id}-desc` : undefined}
        tabIndex={-1}
        onKeyDown={onDialogKeyDown}
        className={cx('relative flex max-h-[92vh] w-full flex-col rounded-t-2xl bg-white shadow-2xl outline-none sm:rounded-2xl dark:bg-slate-900 dark:ring-1 dark:ring-slate-800', sizes[size])}
      >
        <div className="flex items-start justify-between gap-4 border-b border-slate-200 px-5 py-4 dark:border-slate-800">
          <div className="min-w-0">
            <h2 id={`${id}-title`} className="truncate text-base font-semibold text-slate-900 dark:text-white">
              {title}
            </h2>
            {description && (
              <p id={`${id}-desc`} className="mt-0.5 text-sm text-slate-500 dark:text-slate-400">
                {description}
              </p>
            )}
          </div>
          <IconButton label="Close" onClick={onClose}>
            <X className="size-4" />
          </IconButton>
        </div>
        <div className="scroll-thin min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer && <div className="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-3 dark:border-slate-800">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

// --- Toasts and confirmation dialogs ---

type Toast = { id: number; tone: 'success' | 'error' | 'info' | 'warning'; title: string; message?: string }
type ConfirmOpts = { title: string; message: ReactNode; confirmLabel?: string; tone?: 'danger' | 'primary'; requireText?: string }

const UICtx = createContext<{
  toast: (tone: Toast['tone'], title: string, message?: string) => void
  confirm: (o: ConfirmOpts) => Promise<boolean>
} | null>(null)

export function useUI() {
  const c = useContext(UICtx)
  if (!c) throw new Error('UIProvider missing')
  return c
}

export function UIProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const [confirmState, setConfirmState] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(null)
  const [typed, setTyped] = useState('')
  const idRef = useRef(1)
  const confirmInputId = useId()

  const toast = useCallback((tone: Toast['tone'], title: string, message?: string) => {
    const id = idRef.current++
    setToasts((t) => [...t.slice(-4), { id, tone, title, message }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === 'error' ? 8000 : 4500)
  }, [])

  const confirm = useCallback((o: ConfirmOpts) => {
    setTyped('')
    return new Promise<boolean>((resolve) => setConfirmState({ ...o, resolve }))
  }, [])

  const close = (v: boolean) => {
    confirmState?.resolve(v)
    setConfirmState(null)
  }

  const icons = {
    success: <CheckCircle2 aria-hidden="true" className="size-5 text-emerald-500" />,
    error: <XCircle aria-hidden="true" className="size-5 text-rose-500" />,
    info: <Info aria-hidden="true" className="size-5 text-sky-500" />,
    warning: <AlertTriangle aria-hidden="true" className="size-5 text-amber-500" />,
  }

  return (
    <UICtx.Provider value={{ toast, confirm }}>
      {children}
      {createPortal(
        <div aria-live="polite" className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(380px,calc(100vw-2rem))] flex-col gap-2">
          {toasts.map((t) => (
            <div key={t.id} role={t.tone === 'error' ? 'alert' : 'status'} className="pointer-events-auto flex items-start gap-3 rounded-xl border border-slate-200 bg-white p-3.5 shadow-lg dark:border-slate-700 dark:bg-slate-900">
              {icons[t.tone]}
              <div className="min-w-0 flex-1">
                <div className="text-sm font-semibold text-slate-900 dark:text-white">{t.title}</div>
                {t.message && <div className="mt-0.5 text-sm break-words text-slate-500 dark:text-slate-400">{t.message}</div>}
              </div>
              <button type="button" className="text-slate-400 hover:text-slate-600" onClick={() => setToasts((x) => x.filter((y) => y.id !== t.id))} aria-label="Dismiss">
                <X className="size-4" />
              </button>
            </div>
          ))}
        </div>,
        document.body,
      )}
      <Modal
        open={!!confirmState}
        onClose={() => close(false)}
        title={confirmState?.title}
        size="sm"
        footer={
          <>
            <Button onClick={() => close(false)}>Cancel</Button>
            <Button variant={confirmState?.tone === 'primary' ? 'primary' : 'danger'} disabled={!!confirmState?.requireText && typed.trim() !== confirmState.requireText} onClick={() => close(true)}>
              {confirmState?.confirmLabel ?? 'Confirm'}
            </Button>
          </>
        }
      >
        <div className="text-sm text-slate-600 dark:text-slate-300">{confirmState?.message}</div>
        {confirmState?.requireText && (
          <div className="mt-4">
            <label htmlFor={confirmInputId} className="label">
              Type <span className="font-mono normal-case">{confirmState.requireText}</span> to confirm
            </label>
            <input id={confirmInputId} className="input" autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} />
          </div>
        )}
      </Modal>
    </UICtx.Provider>
  )
}

// --- Dropdown menu ---

export type MenuItem = { label: string; icon?: ReactNode; onClick: () => void; danger?: boolean; disabled?: boolean } | 'divider'

export function Menu({ trigger, items, align = 'right' }: { trigger: (open: () => void) => ReactNode; items: MenuItem[]; align?: 'left' | 'right' }) {
  const [pos, setPos] = useState<{ x: number; y: number } | null>(null)
  const ref = useRef<HTMLSpanElement>(null)
  const open = () => {
    const r = ref.current?.getBoundingClientRect()
    if (r) setPos({ x: align === 'right' ? r.right : r.left, y: r.bottom + 4 })
  }
  return (
    <span ref={ref} className="inline-flex">
      {trigger(open)}
      {pos && <ContextMenu x={pos.x} y={pos.y} items={items} onClose={() => setPos(null)} alignRight={align === 'right'} />}
    </span>
  )
}

export function ContextMenu({ x, y, items, onClose, alignRight }: { x: number; y: number; items: MenuItem[]; onClose: () => void; alignRight?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const [style, setStyle] = useState<React.CSSProperties>({ left: x, top: y, visibility: 'hidden' })
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const w = el.offsetWidth
    const h = el.offsetHeight
    let left = alignRight ? x - w : x
    let top = y
    if (left + w > window.innerWidth - 8) left = window.innerWidth - w - 8
    if (top + h > window.innerHeight - 8) top = Math.max(8, y - h - 8)
    setStyle({ left: Math.max(8, left), top })
  }, [x, y, alignRight])
  useEffect(() => {
    const close = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose()
    }
    // Capture phase so Escape closes only the menu, not an enclosing Modal (which skips prevented events).
    const key = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      onClose()
    }
    setTimeout(() => window.addEventListener('mousedown', close), 0)
    window.addEventListener('keydown', key, true)
    window.addEventListener('scroll', onClose, true)
    return () => {
      window.removeEventListener('mousedown', close)
      window.removeEventListener('keydown', key, true)
      window.removeEventListener('scroll', onClose, true)
    }
  }, [onClose])
  // Keyboard: focus the first item on open (returning focus on close), arrows/Home/End move between items.
  useEffect(() => {
    const returnTo = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return () => {
      if (returnTo && returnTo.isConnected && (!document.activeElement || document.activeElement === document.body)) returnTo.focus({ preventScroll: true })
    }
  }, [])
  const positioned = style.visibility !== 'hidden'
  useEffect(() => {
    if (positioned) ref.current?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus({ preventScroll: true })
  }, [positioned])
  const onMenuKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const els = Array.from(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [])
    if (els.length === 0) return
    const i = els.indexOf(document.activeElement as HTMLElement)
    let next: number | null = null
    if (e.key === 'ArrowDown') next = (i + 1) % els.length
    else if (e.key === 'ArrowUp') next = (i - 1 + els.length) % els.length
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = els.length - 1
    else if (e.key === 'Tab') {
      e.preventDefault()
      onClose()
      return
    }
    if (next === null) return
    e.preventDefault()
    els[next].focus()
  }
  return createPortal(
    <div ref={ref} style={style} role="menu" onKeyDown={onMenuKeyDown} className="fixed z-[70] min-w-48 rounded-xl border border-slate-200 bg-white p-1 shadow-xl dark:border-slate-700 dark:bg-slate-900">
      {items.map((it, i) =>
        it === 'divider' ? (
          <div key={i} role="separator" className="my-1 h-px bg-slate-200 dark:bg-slate-800" />
        ) : (
          <button
            key={i}
            type="button"
            role="menuitem"
            tabIndex={-1}
            disabled={it.disabled}
            onClick={() => {
              onClose()
              it.onClick()
            }}
            className={cx('flex w-full items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-left text-sm transition disabled:opacity-40', it.danger ? 'text-rose-600 hover:bg-rose-50 dark:hover:bg-rose-500/10' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-800')}
          >
            <span aria-hidden="true" className="flex size-4 items-center justify-center">
              {it.icon}
            </span>
            {it.label}
          </button>
        ),
      )}
    </div>,
    document.body,
  )
}

export function CopyButton({ text, label = 'Copy', className, size = 'sm' }: { text: string | (() => string); label?: string; className?: string; size?: 'sm' | 'md' }) {
  const [done, setDone] = useState(false)
  const copy = async () => {
    const value = typeof text === 'function' ? text() : text
    try {
      await navigator.clipboard.writeText(value)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = value
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    setDone(true)
    setTimeout(() => setDone(false), 1500)
  }
  return (
    <Button size={size} variant="secondary" className={className} onClick={copy} icon={done ? <CheckCircle2 className="size-3.5 text-emerald-500" /> : undefined}>
      {done ? 'Copied' : label}
    </Button>
  )
}

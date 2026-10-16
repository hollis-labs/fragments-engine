import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Button, Input } from '@hollis-labs/sysop-ui'

/** The bearer is only used for sign-in. The server issues an HttpOnly session
 * cookie so article/media requests work too; no credential goes into storage. */
export function AuthGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<'loading' | 'open' | 'signed-in' | 'locked'>('loading')
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let active = true
    async function check() {
      try {
        const status = await fetch('/v1/auth/status', { credentials: 'same-origin' })
        if (!status.ok) throw new Error('Could not check sign-in status.')
        const { enabled } = await status.json()
        if (!enabled) { if (active) setState('open'); return }
        const session = await fetch('/v1/auth/session', { credentials: 'same-origin' })
        if (active) setState(session.ok ? 'signed-in' : 'locked')
      } catch {
        if (active) { setError('Could not connect to Fragments Engine. Reload to try again.'); setState('locked') }
      }
    }
    void check()
    const expired = () => { setState('locked'); setError('Your session ended. Sign in again.') }
    window.addEventListener('fe-auth-required', expired)
    return () => { active = false; window.removeEventListener('fe-auth-required', expired) }
  }, [])

  async function signIn(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError('')
    try {
      const response = await fetch('/v1/auth/session', {
        method: 'POST', credentials: 'same-origin', headers: { Authorization: `Bearer ${token}` },
      })
      setToken('')
      if (!response.ok) {
        setError(response.status === 401 ? 'That token was not accepted.' : 'Sign-in is unavailable. Check the service and try again.')
        return
      }
      setState('signed-in')
    } catch { setError('Could not connect to Fragments Engine.') }
    finally { setBusy(false) }
  }

  async function signOut() {
    setBusy(true)
    try {
      const response = await fetch('/v1/auth/session', { method: 'DELETE', credentials: 'same-origin' })
      if (!response.ok && response.status !== 401) throw new Error('sign-out failed')
      setState('locked')
      setError('')
    } catch { setError('Could not sign out. Try again.') }
    finally { setBusy(false) }
  }

  if (state === 'loading') return <div className="flex h-full items-center justify-center">Connecting…</div>
  if (state === 'open') return children
  if (state === 'signed-in') return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center justify-end gap-3 border-b border-border px-3 py-1">
        {error && <span role="alert">{error}</span>}
        <Button variant="ghost" size="sm" disabled={busy} onClick={() => void signOut()}>Sign out</Button>
      </div>
      <div className="min-h-0 flex-1">{children}</div>
    </div>
  )
  return (
    <div className="flex h-full items-center justify-center bg-background text-foreground">
      <form onSubmit={signIn} className="flex w-full max-w-sm flex-col gap-4 rounded-lg border border-border p-6">
        <h1 className="text-lg font-semibold">Sign in to Fragments Engine</h1>
        <p className="text-sm text-muted-foreground">Enter your API token to open Sysop.</p>
        <label htmlFor="api-token" className="text-sm">API token</label>
        <Input id="api-token" type="password" autoComplete="off" value={token} onChange={(event) => setToken(event.target.value)} required />
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <Button type="submit" disabled={busy || !token}>{busy ? 'Signing in…' : 'Sign in'}</Button>
      </form>
    </div>
  )
}

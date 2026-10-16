import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AuthGate } from './AuthGate'

const response = (status: number, body?: unknown) => ({ ok: status >= 200 && status < 300, status, json: async () => body })

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('AuthGate', () => {
  it('keeps content hidden until token exchange succeeds and never persists the token', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response(200, { enabled: true })).mockResolvedValueOnce(response(401)).mockResolvedValueOnce(response(204))
    vi.stubGlobal('fetch', fetch)
    const local = vi.spyOn(Storage.prototype, 'setItem')
    render(<AuthGate><div>Private content</div></AuthGate>)
    const input = await screen.findByLabelText('API token')
    expect(screen.queryByText('Private content')).toBeNull()
    fireEvent.change(input, { target: { value: 'synthetic-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('Private content')
    expect(fetch).toHaveBeenLastCalledWith('/v1/auth/session', expect.objectContaining({ method: 'POST', headers: { Authorization: 'Bearer synthetic-token' } }))
    expect(local).not.toHaveBeenCalled()
    local.mockRestore()
    fireEvent(window, new Event('fe-auth-required'))
    await screen.findByLabelText('API token')
    expect(screen.queryByText('Private content')).toBeNull()
  })

  it('rejects a bad token and clears it from the form', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response(200, { enabled: true })).mockResolvedValueOnce(response(401)).mockResolvedValueOnce(response(401)))
    render(<AuthGate><div>Private content</div></AuthGate>)
    const input = await screen.findByLabelText('API token') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'bad-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('That token was not accepted.')
    expect(input.value).toBe('')
    expect(screen.queryByText('Private content')).toBeNull()
  })

  it('restores a cookie session and revokes it on sign-out', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response(200, { enabled: true })).mockResolvedValueOnce(response(204)).mockResolvedValueOnce(response(204))
    vi.stubGlobal('fetch', fetch)
    render(<AuthGate><div>Private content</div></AuthGate>)
    await screen.findByText('Private content')
    fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(screen.queryByText('Private content')).toBeNull())
    expect(fetch).toHaveBeenLastCalledWith('/v1/auth/session', { method: 'DELETE', credentials: 'same-origin' })
  })
})

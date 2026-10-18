import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { SecretField, withSecret, type SecretValue } from '../SecretField'

function Harness({ hasSecret, onValue }: { hasSecret: boolean; onValue: (v: SecretValue) => void }) {
  const [v, setV] = useState<SecretValue>(undefined)
  return (
    <SecretField
      label="API key"
      hasSecret={hasSecret}
      updatedAt={Date.now() - 60_000}
      value={v}
      onChange={(n) => {
        setV(n)
        onValue(n)
      }}
    />
  )
}

describe('SecretField', () => {
  it('shows not set and produces a string to set', () => {
    const onValue = vi.fn()
    render(<Harness hasSecret={false} onValue={onValue} />)
    expect(screen.getByText('Not set')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Clear' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Set' }))
    fireEvent.change(screen.getByLabelText('New API key'), { target: { value: 's3cret' } })
    expect(onValue).toHaveBeenLastCalledWith('s3cret')
    expect(screen.getByLabelText('New API key')).toHaveAttribute('type', 'password')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onValue).toHaveBeenLastCalledWith(undefined)
  })

  it('shows set and produces null to clear, undo returns to keep', () => {
    const onValue = vi.fn()
    render(<Harness hasSecret onValue={onValue} />)
    expect(screen.getByText(/^Set/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(onValue).toHaveBeenLastCalledWith(null)
    expect(screen.getByText('Will be cleared')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Undo clear' }))
    expect(onValue).toHaveBeenLastCalledWith(undefined)
  })

  it('withSecret omits keep and empty values', () => {
    expect(withSecret({ a: 1 }, undefined)).toEqual({ a: 1 })
    expect(withSecret({ a: 1 }, '')).toEqual({ a: 1 })
    expect(withSecret({ a: 1 }, null)).toEqual({ a: 1, secret: null })
    expect(withSecret({ a: 1 }, 'x', 'api_key')).toEqual({ a: 1, api_key: 'x' })
  })
})

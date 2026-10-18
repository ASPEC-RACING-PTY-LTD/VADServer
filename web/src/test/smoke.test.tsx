import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'

function Hello({ name }: { name: string }) {
  return <h1>Hello, {name}</h1>
}

describe('test tooling smoke', () => {
  it('renders a component into jsdom', () => {
    render(<Hello name="VaultDock" />)
    expect(screen.getByRole('heading', { name: 'Hello, VaultDock' })).toBeInTheDocument()
  })
})

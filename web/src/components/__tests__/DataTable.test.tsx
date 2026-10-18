import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { DataTable, type Column } from '../data/DataTable'
import { compareValues, nextSort } from '../data/sort'

type Row = { id: number; name: string; size: number }
const rows: Row[] = [
  { id: 1, name: 'beta', size: 20 },
  { id: 2, name: 'alpha', size: 5 },
  { id: 3, name: 'gamma', size: 10 },
]
const columns: Column<Row>[] = [
  { id: 'name', header: 'Name', cell: (r) => r.name, sortValue: (r) => r.name },
  { id: 'size', header: 'Size', cell: (r) => r.size, sortValue: (r) => r.size, align: 'right' },
]
const names = () => screen.getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell').at(-2)!.textContent)

describe('DataTable', () => {
  it('sorts client-side asc, desc, then original order', () => {
    render(<DataTable label="Things" rows={rows} columns={columns} rowKey={(r) => r.id} />)
    const btn = screen.getByRole('button', { name: /Name/ })
    fireEvent.click(btn)
    expect(names()).toEqual(['alpha', 'beta', 'gamma'])
    expect(screen.getByRole('columnheader', { name: /Name/ })).toHaveAttribute('aria-sort', 'ascending')
    fireEvent.click(btn)
    expect(names()).toEqual(['gamma', 'beta', 'alpha'])
    fireEvent.click(btn)
    expect(names()).toEqual(['beta', 'alpha', 'gamma'])
  })

  it('selects rows and shows bulk actions', () => {
    const onSel = vi.fn()
    render(<DataTable label="Things" rows={rows} columns={columns} rowKey={(r) => r.id} selectable onSelectedChange={onSel} bulkActions={(sel) => <span>bulk:{sel.map((r) => r.name).join(',')}</span>} />)
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select all rows' }))
    expect(onSel).toHaveBeenLastCalledWith(new Set([1, 2, 3]))
    expect(screen.getByText('3 selected')).toBeInTheDocument()
    expect(screen.getByText('bulk:beta,alpha,gamma')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Clear selection' }))
    expect(screen.queryByRole('toolbar')).not.toBeInTheDocument()
  })

  it('activates rows with Enter', () => {
    const onRow = vi.fn()
    render(<DataTable label="Things" rows={rows} columns={columns} rowKey={(r) => r.id} onRowClick={onRow} />)
    const first = screen.getAllByRole('row')[1]
    fireEvent.keyDown(first, { key: 'Enter' })
    expect(onRow).toHaveBeenCalledWith(rows[0])
  })

  it('renders empty, loading, error and load-more states', () => {
    const { rerender } = render(<DataTable label="T" rows={[]} columns={columns} rowKey={(r) => r.id} loading />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
    rerender(<DataTable label="T" rows={[]} columns={columns} rowKey={(r) => r.id} empty={<p>No things</p>} />)
    expect(screen.getByText('No things')).toBeInTheDocument()
    const retry = vi.fn()
    rerender(<DataTable label="T" rows={[]} columns={columns} rowKey={(r) => r.id} error={new Error('boom')} onRetry={retry} />)
    expect(screen.getByRole('alert')).toHaveTextContent('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(retry).toHaveBeenCalled()
    const more = vi.fn()
    rerender(<DataTable label="T" rows={rows} columns={columns} rowKey={(r) => r.id} hasMore onLoadMore={more} />)
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(more).toHaveBeenCalled()
  })
})

describe('sort helpers', () => {
  it('puts empty values last and cycles direction', () => {
    expect(compareValues(null, 1, 'asc')).toBe(1)
    expect(compareValues(null, 1, 'desc')).toBe(1)
    expect(compareValues('a2', 'a10', 'asc')).toBeLessThan(0)
    expect(nextSort(null, 'x')).toEqual({ id: 'x', dir: 'asc' })
    expect(nextSort({ id: 'x', dir: 'desc' }, 'x')).toBeNull()
  })
})

import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { TreeView, type TreeNode } from '../TreeView'

// /a (folder: a1, a2), /b (file)
const tree: Record<string, TreeNode[]> = {
  '': [
    { id: '/a', label: 'a', hasChildren: true },
    { id: '/b', label: 'b' },
  ],
  '/a': [
    { id: '/a/1', label: 'a1' },
    { id: '/a/2', label: 'a2' },
  ],
}
const loader = (n: TreeNode | null) => Promise.resolve(tree[n?.id ?? ''] ?? [])

const item = (name: string) => screen.getByRole('treeitem', { name })

describe('TreeView', () => {
  it('loads roots, lazily loads children and supports arrow-key navigation', async () => {
    const load = vi.fn(loader)
    render(<TreeView ariaLabel="Files" onLoadChildren={load} />)
    expect(await screen.findByRole('tree', { name: 'Files' })).toBeInTheDocument()
    expect(load).toHaveBeenCalledWith(null)
    expect(item('a')).toHaveAttribute('aria-expanded', 'false')
    expect(item('a')).toHaveAttribute('tabindex', '0')

    item('a').focus()
    fireEvent.keyDown(item('a'), { key: 'ArrowRight' })
    expect(await screen.findByRole('treeitem', { name: 'a1' })).toHaveAttribute('aria-level', '2')
    expect(item('a')).toHaveAttribute('aria-expanded', 'true')
    expect(load).toHaveBeenCalledTimes(2)

    fireEvent.keyDown(item('a'), { key: 'ArrowDown' })
    await waitFor(() => expect(item('a1')).toHaveFocus())
    fireEvent.keyDown(item('a1'), { key: 'ArrowLeft' })
    await waitFor(() => expect(item('a')).toHaveFocus())
    fireEvent.keyDown(item('a'), { key: 'ArrowLeft' })
    await waitFor(() => expect(screen.queryByRole('treeitem', { name: 'a1' })).not.toBeInTheDocument())
    fireEvent.keyDown(item('a'), { key: 'End' })
    await waitFor(() => expect(item('b')).toHaveFocus())
  })

  it('computes tri-state checks and splits an inherited check', async () => {
    const onChange = vi.fn()
    const { rerender } = render(<TreeView ariaLabel="Files" onLoadChildren={loader} checkable checked={['/a/1']} onCheckedChange={onChange} defaultExpanded={['/a']} />)
    await screen.findByRole('treeitem', { name: 'a1' })
    expect(item('a')).toHaveAttribute('aria-checked', 'mixed')
    expect(item('a1')).toHaveAttribute('aria-checked', 'true')
    expect(item('a2')).toHaveAttribute('aria-checked', 'false')

    // Checking the last sibling collapses the group into its parent.
    item('a2').focus()
    fireEvent.keyDown(item('a2'), { key: ' ' })
    expect(onChange).toHaveBeenLastCalledWith(['/a'])

    rerender(<TreeView ariaLabel="Files" onLoadChildren={loader} checkable checked={['/a']} onCheckedChange={onChange} defaultExpanded={['/a']} />)
    expect(item('a1')).toHaveAttribute('aria-checked', 'true')
    // Unchecking a child of a checked folder keeps its siblings.
    item('a1').focus()
    fireEvent.keyDown(item('a1'), { key: ' ' })
    expect(onChange).toHaveBeenLastCalledWith(['/a/2'])
  })
})

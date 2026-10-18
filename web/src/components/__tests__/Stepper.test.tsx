import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Stepper, type StepDef } from '../Stepper'

function steps(valid: { current: boolean }): StepDef[] {
  return [
    { id: 'a', title: 'Source', content: <p>source body</p>, validate: () => (valid.current ? null : 'Pick a source') },
    { id: 'b', title: 'Hidden', content: <p>hidden body</p>, hidden: true },
    { id: 'c', title: 'Review', content: <p>review body</p> },
  ]
}

describe('Stepper', () => {
  it('blocks Next until the step validates and skips hidden steps', async () => {
    const valid = { current: false }
    render(<Stepper steps={steps(valid)} onFinish={() => {}} />)
    expect(screen.getByText('source body')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Pick a source')
    expect(screen.getByText('source body')).toBeInTheDocument()

    valid.current = true
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('review body')).toBeInTheDocument()
    expect(screen.queryByText('hidden body')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Finish' })).toBeInTheDocument()
  })

  it('calls onFinish on the last step and surfaces its error', async () => {
    const onFinish = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValueOnce(undefined)
    render(<Stepper steps={steps({ current: true })} onFinish={onFinish} index={1} />)
    fireEvent.click(screen.getByRole('button', { name: 'Finish' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
    fireEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(onFinish).toHaveBeenCalledTimes(2))
  })

  it('marks the current step and goes back', async () => {
    render(<Stepper steps={steps({ current: true })} onFinish={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await screen.findByText('review body')
    expect(screen.getByRole('navigation').querySelector('[aria-current="step"]')).toHaveTextContent('Review')
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(screen.getByText('source body')).toBeInTheDocument()
  })
})

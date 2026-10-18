import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { ScheduleEditor, defaultSchedule, describeSchedule, isoWeekday, validateSchedule, type Schedule } from '../ScheduleEditor'

const base: Schedule = { type: 'manual', expr: '', at: '02:00', weekdays: null, every_minutes: 0, timezone: '', catch_up: '', jitter_minutes: 0 }
const sch = (p: Partial<Schedule>): Schedule => ({ ...base, ...p })

function Harness({ initial }: { initial: Schedule }) {
  const [v, setV] = useState<Schedule>(initial)
  return (
    <>
      <ScheduleEditor value={v} onChange={setV} />
      <output data-testid="value">{JSON.stringify(v)}</output>
    </>
  )
}
const value = () => JSON.parse(screen.getByTestId('value').textContent ?? '{}') as Schedule

describe('ScheduleEditor', () => {
  it('validates schedules like sdk/schedule', () => {
    expect(validateSchedule(sch({ type: 'manual' }))).toBeNull()
    expect(validateSchedule(sch({ type: 'interval', every_minutes: 1 }))).toMatch(/at least 5/)
    expect(validateSchedule(sch({ type: 'interval', every_minutes: 44641 }))).toMatch(/31 days/)
    expect(validateSchedule(sch({ type: 'interval', every_minutes: 30, jitter_minutes: 30 }))).toMatch(/less than the interval/)
    expect(validateSchedule(sch({ type: 'daily', at: '25:00' }))).toMatch(/HH:MM/)
    expect(validateSchedule(sch({ type: 'weekly', weekdays: [], at: '09:00' }))).toMatch(/at least one day/)
    expect(validateSchedule(sch({ type: 'weekly', weekdays: [0], at: '09:00' }))).toMatch(/Invalid weekday/)
    expect(validateSchedule(sch({ type: 'weekly', weekdays: [7], at: '09:00' }))).toBeNull()
    expect(validateSchedule(sch({ type: 'cron', expr: '0 2 * *' }))).toMatch(/5 fields/)
    expect(validateSchedule(sch({ type: 'cron', expr: '*/15 * * * mon-fri' }))).toBeNull()
    expect(validateSchedule(sch({ type: 'daily', at: '09:00', jitter_minutes: 1441 }))).toMatch(/Jitter/)
    expect(validateSchedule(sch({ type: 'daily', at: '09:00', timezone: 'Not/AZone' }))).toMatch(/time zone/)
    expect(validateSchedule(sch({ type: 'daily', at: '09:00', timezone: 'Australia/Sydney', catch_up: 'skip' }))).toBeNull()
  })

  it('describes schedules with ISO weekdays', () => {
    expect(describeSchedule(sch({ type: 'interval', every_minutes: 120 }))).toBe('Every 2 hours')
    expect(describeSchedule(sch({ type: 'weekly', weekdays: [5, 1, 2, 3, 4], at: '09:00' }))).toBe('Weekdays at 09:00')
    expect(describeSchedule(sch({ type: 'weekly', weekdays: [6, 7], at: '09:00' }))).toBe('Weekends at 09:00')
    expect(describeSchedule(sch({ type: 'weekly', weekdays: [1, 7], at: '09:00', timezone: 'UTC' }))).toBe('Mon, Sun at 09:00 (UTC)')
    expect(describeSchedule(sch({ type: 'manual' }))).toBe('Manual only')
    expect(isoWeekday(0)).toBe(7)
    expect(isoWeekday(3)).toBe(3)
  })

  it('round-trips the wire JSON shape', () => {
    const wire = { type: 'weekly', expr: '', at: '03:00', weekdays: [1, 3], every_minutes: 0, timezone: 'Local', catch_up: 'skip', jitter_minutes: 10 }
    const s = defaultSchedule('weekly', wire as Schedule)
    expect(Object.keys(s).sort()).toEqual(Object.keys(wire).sort())
    expect(defaultSchedule('cron', sch({ at: '03:30' })).expr).toBe('30 3 * * *')
  })

  it('switches types, carrying fields over, and toggles weekdays', () => {
    render(<Harness initial={sch({ type: 'daily', at: '03:30', catch_up: 'skip' })} />)
    fireEvent.click(screen.getByRole('radio', { name: 'Weekly' }))
    expect(value()).toMatchObject({ type: 'weekly', weekdays: [1, 2, 3, 4, 5], at: '03:30', catch_up: 'skip' })

    fireEvent.click(screen.getByRole('button', { name: 'Monday' }))
    expect(value().weekdays).toEqual([2, 3, 4, 5])
    fireEvent.click(screen.getByRole('button', { name: 'Sunday' }))
    expect(value().weekdays).toEqual([2, 3, 4, 5, 7])
    expect(screen.getByRole('button', { name: 'Sunday' })).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(screen.getByRole('radio', { name: 'Every…' }))
    expect(value()).toMatchObject({ type: 'interval', every_minutes: 60 })
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Minutes' }), { target: { value: '2' } })
    expect(screen.getByRole('alert')).toHaveTextContent(/at least 5/)

    fireEvent.click(screen.getByRole('radio', { name: 'Cron' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Cron expression' }), { target: { value: '0 4 * * sun' } })
    expect(value()).toMatchObject({ type: 'cron', expr: '0 4 * * sun' })
    expect(screen.queryByRole('alert')).toBeNull()

    fireEvent.click(screen.getByRole('radio', { name: 'Manual' }))
    expect(value().type).toBe('manual')
    expect(screen.getByText(/runs only when started manually/)).toBeInTheDocument()
  })
})

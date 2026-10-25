import { describe, expect, it } from 'vitest'
import { applyEdit, clipAt, cropForAspect, editKey, emptyProject, estimateSize, exportName, formatTime, historyReducer, initHistory, laneLayout, loadProject, outputDuration, parseTime, type MediaInfo, type Project } from '../model'

const info: MediaInfo = { duration: 600, width: 1920, height: 1080, fps: 60, video_codec: 'h264', audio_tracks: 1, sample_rate: 48000, bitrate: 12.2e6, video_bitrate: 12e6, audio_bitrate: 192e3, format: 'mp4', size: 915e6 }

function withClips(...ranges: [number, number][]): Project {
  let p = emptyProject('MedalTVScreenRecording20250419003227.mp4')
  for (const [s, e] of ranges) p = applyEdit(p, { type: 'add', start: s, end: e }, 600).project
  return p
}

describe('video editor model', () => {
  it('numbers selections and keeps numbers through splits and removals', () => {
    let p = withClips([15, 32], [130, 147], [342, 365])
    expect(p.clips.map((c) => c.number)).toEqual([1, 2, 3])
    expect(p.name).toBe('MedalTVScreenRecording20250419003227')
    p = applyEdit(p, { type: 'remove', id: p.clips[1].id }, 600).project
    const r = applyEdit(p, { type: 'split', id: p.clips[0].id, at: 20 }, 600)
    p = r.project
    expect(p.clips.map((c) => [c.number, c.start, c.end])).toEqual([
      [1, 15, 20],
      [4, 20, 32],
      [3, 342, 365],
    ])
    expect(r.select).toBe(p.clips[1].id)
    // A split too close to an edge does nothing.
    expect(applyEdit(p, { type: 'split', id: p.clips[0].id, at: 15.05 }, 600).project).toBe(p)
  })

  it('keeps trims valid and drops freeze frames that fall outside', () => {
    let p = withClips([10, 20])
    const id = p.clips[0].id
    p = applyEdit(p, { type: 'update', id, patch: { freezes: [{ at: 18, duration: 2 }] } }, 600).project
    p = applyEdit(p, { type: 'update', id, patch: { end: 15 } }, 600).project
    expect(p.clips[0].freezes).toEqual([])
    p = applyEdit(p, { type: 'update', id, patch: { start: 30 } }, 600).project
    expect(p.clips[0].start).toBeCloseTo(14.9)
    expect(p.clips[0].end).toBe(15)
    p = applyEdit(p, { type: 'update', id, patch: { end: 9999 } }, 600).project
    expect(p.clips[0].end).toBe(600)
  })

  it('reorders without renumbering', () => {
    let p = withClips([1, 2], [3, 4])
    p = applyEdit(p, { type: 'move-order', id: p.clips[1].id, dir: -1 }, 600).project
    expect(p.clips.map((c) => c.number)).toEqual([2, 1])
  })

  it('undoes and redoes, merging a drag into one step', () => {
    let h = initHistory(withClips([10, 20]))
    const id = h.present.clips[0].id
    const key = editKey({ type: 'update', id, patch: { end: 0 } }) + '#1'
    for (const end of [21, 22, 23]) h = historyReducer(h, { type: 'edit', edit: { type: 'update', id, patch: { end } }, duration: 600, key })
    expect(h.present.clips[0].end).toBe(23)
    expect(h.past.length).toBe(1)
    expect(h.dirty).toBe(true)
    h = historyReducer(h, { type: 'undo' })
    expect(h.present.clips[0].end).toBe(20)
    h = historyReducer(h, { type: 'redo' })
    expect(h.present.clips[0].end).toBe(23)
    // A new drag of the same edge is its own step.
    h = historyReducer(h, { type: 'edit', edit: { type: 'update', id, patch: { end: 30 } }, duration: 600, key: key.replace('#1', '#2') })
    expect(h.past.length).toBe(2)
    h = historyReducer(h, { type: 'saved' })
    expect(h.dirty).toBe(false)
  })

  it('lays overlapping selections out in lanes', () => {
    const p = withClips([0, 10], [5, 15], [12, 20], [11, 13])
    const { lanes, count } = laneLayout(p.clips)
    expect(count).toBe(3)
    expect(p.clips.map((c) => lanes.get(c.id))).toEqual([0, 1, 2, 0])
    expect(clipAt(p.clips, 7, p.clips[1].id)?.id).toBe(p.clips[1].id)
    expect(clipAt(p.clips, 7, null)?.id).toBe(p.clips[0].id)
  })

  it('loads damaged or old drafts safely', () => {
    const p = loadProject({ name: 'Goals', quality: 'nope', clips: [{ start: 5, end: 9, number: 3, name: 'A' }, { start: 4, end: 4 }, null, { start: 1, end: 2, number: 3 }, { start: 700, end: 800 }] }, 'x.mp4', 600)
    expect(p.name).toBe('Goals')
    expect(p.quality).toBe('high')
    expect(p.clips.map((c) => c.number)).toEqual([3, 4])
    expect(p.nextNumber).toBe(5)
    expect(loadProject('junk', 'x.mp4', 600).clips).toEqual([])
  })

  it('estimates sizes like the server and names exports', () => {
    const p = withClips([0, 60])
    const c = p.clips[0]
    const copy = estimateSize(c, 'copy', info)
    expect(copy).toBeGreaterThan(85e6)
    expect(copy).toBeLessThan(95e6)
    expect(estimateSize(c, 'small', info)).toBeLessThan(copy)
    const fast = { ...c, speed: 2, freezes: [{ at: 30, duration: 3 }] }
    expect(outputDuration(fast)).toBe(33)
    expect(exportName(p, { ...c, name: 'Rocket: Goal?' })).toBe('Rocket- Goal-')
    expect(exportName(p, c)).toBe('MedalTVScreenRecording20250419003227 #1')
  })

  it('fits aspect-ratio crops inside the frame', () => {
    const r = cropForAspect('9:16', info, null)!
    expect(r.h).toBe(1)
    expect((r.w * 1920) / (r.h * 1080)).toBeCloseTo(9 / 16)
    expect(r.x).toBeCloseTo((1 - r.w) / 2)
    const sq = cropForAspect('1:1', info, { x: 0.9, y: 0, w: 0.1, h: 0.1 })!
    expect(sq.x + sq.w).toBeLessThanOrEqual(1)
    expect(cropForAspect('original', info, sq)).toBeNull()
  })

  it('formats and parses times', () => {
    expect(formatTime(75.5)).toBe('1:15.50')
    expect(formatTime(3723, 0)).toBe('1:02:03')
    expect(parseTime('1:15.5')).toBe(75.5)
    expect(parseTime('90')).toBe(90)
    expect(parseTime('1:x')).toBeNull()
  })
})

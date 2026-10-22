import { beforeEach, describe, expect, it, vi } from 'vitest'
import { HostEventSource } from '../../../../subprojects/cpr-console/overrides/src/sub2api/events'

const host = vi.hoisted(() => ({ stream: vi.fn() }))
vi.mock('../../../../subprojects/cpr-console/overrides/src/sub2api/bridge', () => ({ hostBridge: () => host }))

describe('CPR authenticated management event streams', () => {
  beforeEach(() => host.stream.mockReset())
  it('decodes split UTF-8/SSE frames and cancels the upstream stream when closed', async () => {
    const cancel = vi.fn()
    let input!: ReadableStreamDefaultController<Uint8Array>
    let signal!: AbortSignal
    host.stream.mockImplementation((_path, value) => {
      signal = value
      return Promise.resolve(new ReadableStream({ start(controller) { input = controller }, cancel }))
    })
    const source = new HostEventSource('/api/admin/accounts/connection-test?accountId=demo')
    const messages: MessageEvent<string>[] = []
    source.onmessage = event => messages.push(event)
    await vi.waitFor(() => expect(source.readyState).toBe(1))
    const text = new TextEncoder().encode('id: run-1\r\ndata: 出口连接\r\ndata: 已确认\r\n\r\ndata: 完成\r\n\r\n')
    input.enqueue(text.slice(0, 20))
    input.enqueue(text.slice(20, 27))
    input.enqueue(text.slice(27))
    await vi.waitFor(() => expect(messages).toHaveLength(2))
    expect(messages[0].data).toBe('出口连接\n已确认')
    expect(messages[0].lastEventId).toBe('run-1')
    source.close()
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledOnce())
    expect(signal.aborted).toBe(true)
    expect(source.readyState).toBe(2)
  })
  it('closes an oversized stream instead of keeping its network reader alive', async () => {
    const cancel = vi.fn()
    host.stream.mockImplementation(() => Promise.resolve(new ReadableStream({
      start(controller) { controller.enqueue(new Uint8Array(1024 * 1024 + 1)) }, cancel,
    })))
    const source = new HostEventSource('/api/admin/accounts/connection-test?accountId=demo')
    const failed = vi.fn()
    source.onerror = failed
    await vi.waitFor(() => expect(failed).toHaveBeenCalledOnce())
    expect(source.readyState).toBe(2)
    expect(cancel).toHaveBeenCalledOnce()
  })
})

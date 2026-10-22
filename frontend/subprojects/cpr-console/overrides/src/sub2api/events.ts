import { hostBridge } from './bridge'

// 仅替换内嵌控制台的 EventSource：认证与连接期限由宿主持有。
export class HostEventSource extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2
  readonly url: string
  readonly withCredentials: boolean
  readyState = 0
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  private controller = new AbortController()
  private reader?: ReadableStreamDefaultReader<Uint8Array>
  private lastId = ''
  constructor(input: string | URL, options?: EventSourceInit) {
    super()
    const url = new URL(String(input), window.location.origin)
    if (url.origin !== window.location.origin) throw new Error('Unsupported event stream origin')
    this.url = url.href
    this.withCredentials = Boolean(options?.withCredentials)
    void this.read(url.pathname + url.search)
  }
  close() {
    this.readyState = 2
    this.controller.abort()
    void this.reader?.cancel().catch(() => {})
  }
  private async read(path: string) {
    try {
      const stream = await hostBridge().stream(path, this.controller.signal)
      if (this.controller.signal.aborted) return
      this.reader = stream.getReader()
      this.readyState = 1
      const open = new Event('open')
      this.dispatchEvent(open)
      this.onopen?.(open)
      const decoder = new TextDecoder()
      let pending = ''
      while (!this.controller.signal.aborted) {
        const next = await this.reader.read()
        pending += decoder.decode(next.value, { stream: !next.done })
        pending = pending.replace(/\r\n/g, '\n')
        if (pending.length > 1024 * 1024) throw new Error('Event stream frame exceeds its limit')
        let boundary = pending.indexOf('\n\n')
        while (boundary >= 0) {
          this.frame(pending.slice(0, boundary))
          pending = pending.slice(boundary + 2)
          boundary = pending.indexOf('\n\n')
        }
        if (next.done) break
      }
      if (!this.controller.signal.aborted) this.fail()
    } catch {
      if (!this.controller.signal.aborted) this.fail()
    } finally { this.reader?.releaseLock() }
  }
  private frame(text: string) {
    let type = 'message'
    const data: string[] = []
    for (const line of text.split('\n')) {
      if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
      else if (line.startsWith('event:')) type = line.slice(6).trim() || 'message'
      else if (line.startsWith('id:')) {
        const id = line.slice(3).replace(/^ /, '')
        if (!id.includes('\0')) this.lastId = id
      }
    }
    if (!data.length) return
    const event = new MessageEvent<string>(type, { data: data.join('\n'), origin: window.location.origin, lastEventId: this.lastId })
    this.dispatchEvent(event)
    if (type === 'message') this.onmessage?.(event)
  }
  private fail() {
    this.readyState = 2
    this.controller.abort()
    void this.reader?.cancel().catch(() => {})
    const error = new Event('error')
    this.dispatchEvent(error)
    this.onerror?.(error)
  }
}
export function installHostEvents() {
  window.EventSource = HostEventSource as unknown as typeof EventSource
}

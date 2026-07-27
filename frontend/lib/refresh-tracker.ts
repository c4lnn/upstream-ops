export type RefreshCycleResult = "success" | "failed"

export class RefreshRequestTracker {
  private activeCycle: number | null = null
  private pending = 0
  private failed = false
  private seen = new WeakSet<Promise<unknown>>()

  constructor(private readonly onSettled: (result: RefreshCycleResult) => void) {}

  start(cycle: number) {
    this.activeCycle = cycle
    this.pending = 0
    this.failed = false
    this.seen = new WeakSet()
  }

  track(cycle: number, request: Promise<unknown>) {
    if (this.activeCycle !== cycle || this.seen.has(request)) return
    this.seen.add(request)
    this.pending++
    void request.then(
      () => this.finishRequest(cycle, false),
      () => this.finishRequest(cycle, true),
    )
  }

  finishIfIdle(cycle: number) {
    if (this.activeCycle === cycle && this.pending === 0) this.finishCycle()
  }

  cancel() {
    this.activeCycle = null
    this.pending = 0
  }

  private finishRequest(cycle: number, failed: boolean) {
    if (this.activeCycle !== cycle) return
    this.failed ||= failed
    this.pending--
    if (this.pending === 0) this.finishCycle()
  }

  private finishCycle() {
    const result = this.failed ? "failed" : "success"
    this.activeCycle = null
    this.onSettled(result)
  }
}

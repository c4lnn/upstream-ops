import type { ProgressEvent } from "./sync-stream"

export const PROGRESSIVE_REFRESH_DELAY_MS = 300

export function isAccountTerminalEvent(event: ProgressEvent) {
  return event.scope === "account" && (event.stage === "done" || event.stage === "error")
}

export class ProgressiveRefreshScheduler {
  private timer: ReturnType<typeof setTimeout> | null = null

  constructor(
    private readonly refresh: () => void,
    private readonly delayMs = PROGRESSIVE_REFRESH_DELAY_MS,
  ) {}

  schedule() {
    this.cancel()
    this.timer = setTimeout(() => {
      this.timer = null
      this.refresh()
    }, this.delayMs)
  }

  cancel() {
    if (this.timer == null) return
    clearTimeout(this.timer)
    this.timer = null
  }

  finalize(finalRefresh: () => void) {
    this.cancel()
    finalRefresh()
  }
}

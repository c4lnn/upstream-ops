import assert from "node:assert/strict"
import test from "node:test"
import {
  isAccountTerminalEvent,
  ProgressiveRefreshScheduler,
} from "../lib/progressive-refresh"
import type { ProgressEvent } from "../lib/sync-stream"

function event(patch: Partial<ProgressEvent>): ProgressEvent {
  return {
    stage: "done",
    message: "同步完成",
    time: new Date().toISOString(),
    scope: "account",
    account_id: 1,
    ...patch,
  }
}

function wait(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

test("只有账号级 done 和 error 是渐进刷新终态", () => {
  assert.equal(isAccountTerminalEvent(event({ stage: "done" })), true)
  assert.equal(isAccountTerminalEvent(event({ stage: "error" })), true)
  assert.equal(isAccountTerminalEvent(event({ stage: "balance" })), false)
  assert.equal(isAccountTerminalEvent(event({ scope: "operation", stage: "done" })), false)
})

test("短时间连续账号终态合并成一次尾沿刷新", async () => {
  let refreshes = 0
  const scheduler = new ProgressiveRefreshScheduler(() => refreshes++, 10)

  scheduler.schedule()
  scheduler.schedule()
  await wait(25)

  assert.equal(refreshes, 1)
})

test("最终完整刷新前可以取消尚未执行的渐进刷新", async () => {
  let progressiveRefreshes = 0
  let finalRefreshes = 0
  const scheduler = new ProgressiveRefreshScheduler(() => progressiveRefreshes++, 10)

  scheduler.schedule()
  scheduler.finalize(() => finalRefreshes++)
  await wait(25)

  assert.equal(progressiveRefreshes, 0)
  assert.equal(finalRefreshes, 1)
})

import assert from "node:assert/strict"
import test from "node:test"
import { RefreshRequestTracker, type RefreshCycleResult } from "../lib/refresh-tracker"

function deferred() {
  let resolve!: () => void
  let reject!: (reason: Error) => void
  const promise = new Promise<void>((ok, fail) => {
    resolve = ok
    reject = fail
  })
  return { promise, resolve, reject }
}

test("等待动态登记的请求并对共享 Promise 去重", async () => {
  const results: RefreshCycleResult[] = []
  const tracker = new RefreshRequestTracker((result) => results.push(result))
  const first = deferred()
  const second = deferred()
  tracker.start(1)
  tracker.track(1, first.promise)
  tracker.track(1, first.promise)
  tracker.track(1, second.promise)
  first.resolve()
  await Promise.resolve()
  assert.deepEqual(results, [])
  second.resolve()
  await Promise.resolve()
  assert.deepEqual(results, ["success"])
})

test("任一请求失败时本轮结果失败", async () => {
  const results: RefreshCycleResult[] = []
  const tracker = new RefreshRequestTracker((result) => results.push(result))
  const request = deferred()
  tracker.start(2)
  tracker.track(2, request.promise)
  request.reject(new Error("failed"))
  await Promise.resolve()
  assert.deepEqual(results, ["failed"])
})

test("取消后忽略尚未完成的请求", async () => {
  const results: RefreshCycleResult[] = []
  const tracker = new RefreshRequestTracker((result) => results.push(result))
  const request = deferred()
  tracker.start(3)
  tracker.track(3, request.promise)
  tracker.cancel()
  request.resolve()
  await Promise.resolve()
  assert.deepEqual(results, [])
})

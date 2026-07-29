import assert from "node:assert/strict"
import test from "node:test"
import {
  advanceRefreshVersion,
  initialRefreshVersions,
  isTrackedRefresh,
  refreshVersionKey,
} from "../lib/refresh-scopes"

test("命名作用域刷新不推进全局版本，也不进入手动刷新跟踪", () => {
  const next = advanceRefreshVersion(initialRefreshVersions, "monitor-snapshots")

  assert.equal(next.global, 0)
  assert.equal(next.scopes["monitor-snapshots"], 1)
  assert.equal(isTrackedRefresh({ scope: "monitor-snapshots" }), false)
})

test("完整刷新推进所有查询共享的全局版本并进入跟踪", () => {
  const next = advanceRefreshVersion(initialRefreshVersions)

  assert.equal(next.global, 1)
  assert.equal(next.scopes["monitor-snapshots"], 0)
  assert.equal(isTrackedRefresh({ notify: true }), true)
})

test("作用域查询键同时响应全局刷新和命名作用域刷新", () => {
  const scoped = advanceRefreshVersion(initialRefreshVersions, "monitor-snapshots")
  const global = advanceRefreshVersion(scoped)

  assert.equal(refreshVersionKey(initialRefreshVersions, "monitor-snapshots"), "0:0")
  assert.equal(refreshVersionKey(scoped, "monitor-snapshots"), "0:1")
  assert.equal(refreshVersionKey(global, "monitor-snapshots"), "1:1")
  assert.equal(refreshVersionKey(scoped), "0")
})

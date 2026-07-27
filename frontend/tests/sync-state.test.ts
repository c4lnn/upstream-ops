import assert from "node:assert/strict"
import test from "node:test"
import { emptySyncViewState, failSyncOperation, reduceSyncEvent, siteProgressLabel, startSiteSync, startSyncOperation } from "../lib/sync-state"

test("账号阶段、站点进度和操作摘要按作用域归约", () => {
  let state = startSyncOperation(emptySyncViewState)
  state = reduceSyncEvent(state, {
    stage: "rates",
    message: "拉取分组倍率…",
    time: new Date().toISOString(),
    scope: "account",
    site_id: 3,
    account_id: 7,
    account_alias: "primary",
    index: 2,
    total: 5,
  })
  assert.equal(state.accounts[7].running, true)
  assert.equal(siteProgressLabel(state.sites[3]), "同步 2/5 · primary · 拉取分组倍率…")

  state = reduceSyncEvent(state, {
    stage: "error",
    message: "部分同步完成 · 成功 1 / 失败 1",
    ok: false,
    time: new Date().toISOString(),
    scope: "operation",
    data: { status: "partial", success_count: 1, failed_count: 1, items: [] },
  })
  assert.equal(state.operation.running, false)
  assert.equal(state.operation.summary?.status, "partial")
})

test("传输失败解除 operation loading", () => {
  const state = failSyncOperation(startSiteSync(emptySyncViewState, 3, "alpha"), "连接中断")
  assert.equal(state.operation.running, false)
  assert.equal(state.sites[3].running, false)
  assert.equal(state.operation.latest?.message, "连接中断")
})

test("站点同步点击后立即显示准备状态", () => {
  const state = startSiteSync(emptySyncViewState, 9, "alpha")
  assert.equal(state.operation.running, true)
  assert.equal(state.sites[9].running, true)
  assert.equal(siteProgressLabel(state.sites[9]), "准备同步…")
})

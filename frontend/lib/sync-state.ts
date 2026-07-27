import type { ProgressEvent, SyncSummary } from "./sync-stream"

export interface AccountSyncState {
  running: boolean
  latest: ProgressEvent | null
  failed: boolean
}

export interface SiteSyncState {
  running: boolean
  latest: ProgressEvent | null
  failed: boolean
}

export interface OperationSyncState {
  running: boolean
  latest: ProgressEvent | null
  summary: SyncSummary | null
}

export interface SyncViewState {
  accounts: Record<number, AccountSyncState>
  sites: Record<number, SiteSyncState>
  operation: OperationSyncState
}

export const emptySyncViewState: SyncViewState = {
  accounts: {},
  sites: {},
  operation: { running: false, latest: null, summary: null },
}

export function startSyncOperation(state: SyncViewState, message = "准备同步…"): SyncViewState {
  return {
    ...state,
    operation: {
      running: true,
      latest: { stage: "session", message, time: new Date().toISOString(), scope: "operation" },
      summary: null,
    },
  }
}

export function startSiteSync(state: SyncViewState, siteID: number, siteName: string): SyncViewState {
  const event: ProgressEvent = {
    stage: "session",
    message: "准备同步…",
    time: new Date().toISOString(),
    scope: "site",
    site_id: siteID,
    site_name: siteName,
  }
  return {
    ...startSyncOperation(state, `准备同步 ${siteName}…`),
    sites: {
      ...state.sites,
      [siteID]: { running: true, latest: event, failed: false },
    },
  }
}

export function reduceSyncEvent(state: SyncViewState, event: ProgressEvent): SyncViewState {
  const next: SyncViewState = {
    accounts: state.accounts,
    sites: state.sites,
    operation: state.operation,
  }

  if (event.account_id != null) {
    const previous = state.accounts[event.account_id]
    const terminal = isTerminal(event, "account")
    next.accounts = {
      ...state.accounts,
      [event.account_id]: {
        running: !terminal,
        latest: event,
        failed: Boolean(previous?.failed || event.ok === false || event.stage === "error"),
      },
    }
  }

  if (event.site_id != null) {
    const previous = state.sites[event.site_id]
    const terminal = isTerminal(event, "site")
    next.sites = {
      ...state.sites,
      [event.site_id]: {
        running: event.scope === "account" ? true : !terminal,
        latest: event,
        failed: Boolean(previous?.failed || (event.scope === "site" && (event.ok === false || event.stage === "error"))),
      },
    }
  }

  if (event.scope === "operation") {
    next.operation = {
      running: false,
      latest: event,
      summary: event.data ?? null,
    }
  } else if (state.operation.running) {
    next.operation = { ...state.operation, latest: event }
  }

  return next
}

export function failSyncOperation(state: SyncViewState, message: string): SyncViewState {
  const accounts = Object.fromEntries(
    Object.entries(state.accounts).map(([id, item]) => [id, { ...item, running: false }]),
  )
  const sites = Object.fromEntries(
    Object.entries(state.sites).map(([id, item]) => [id, { ...item, running: false }]),
  )
  return {
    ...state,
    accounts,
    sites,
    operation: {
      running: false,
      summary: null,
      latest: { stage: "error", message, ok: false, time: new Date().toISOString(), scope: "operation" },
    },
  }
}

function isTerminal(event: ProgressEvent, scope: "account" | "site") {
  return event.scope === scope && (event.stage === "done" || event.stage === "error")
}

export function operationSummaryLabel(summary: SyncSummary | null) {
  if (!summary) return null
  if (summary.status === "success") return `同步完成 · 成功 ${summary.success_count}`
  if (summary.status === "partial") return `部分同步完成 · 成功 ${summary.success_count} / 失败 ${summary.failed_count}`
  return `同步失败 · 失败 ${summary.failed_count}`
}

export function operationProgressLabel(state: OperationSyncState) {
  if (!state.running) return operationSummaryLabel(state.summary)
  const event = state.latest
  if (!event) return "准备同步…"
  const site = event.site_index && event.site_total ? `站点 ${event.site_index}/${event.site_total}` : event.site_name
  const account = event.index && event.total ? `账号 ${event.index}/${event.total}` : null
  return [site, account, event.account_alias, event.message].filter(Boolean).join(" · ")
}

export function siteProgressLabel(state?: SiteSyncState) {
  const event = state?.latest
  if (!event) return null
  if (event.scope === "site") return event.message
  const position = event.index && event.total ? `${event.index}/${event.total}` : ""
  return [position ? `同步 ${position}` : "同步中", event.account_alias, event.message].filter(Boolean).join(" · ")
}

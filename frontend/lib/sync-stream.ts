/**
 * SSE 流读取：用 fetch 而非 EventSource，以便附带 Authorization Bearer 头。
 */

import { getToken, notifyUnauthorized } from "@/lib/api"

export type ProgressStage =
  | "captcha"
  | "session"
  | "login"
  | "balance"
  | "cost"
  | "subscription"
  | "rates"
  | "done"
  | "error"

export interface ProgressEvent {
  stage: ProgressStage
  message: string
  ok?: boolean
  data?: SyncSummary
  time: string
  scope?: "account" | "site" | "operation"
  account_id?: number
  account_alias?: string
  index?: number
  total?: number
  site_id?: number
  site_name?: string
  site_index?: number
  site_total?: number
}

export interface SyncAccountResult {
  account_id: number
  account_name: string
  success: boolean
  error?: string
}

export interface SyncSummary {
  status: "success" | "partial" | "failed"
  success_count: number
  failed_count: number
  items: SyncAccountResult[]
  error?: string
}

export interface SyncOptions {
  onEvent: (ev: ProgressEvent) => void
  signal?: AbortSignal
}

/**
 * streamSSE 通用 SSE 读取器：POST 指定 path，逐条消费 data 帧。
 * 401 → 触发全局登出回调；其他非 2xx 抛错。
 */
async function streamSSE(
  path: string,
  { onEvent, signal }: SyncOptions,
): Promise<void> {
  const token = getToken()
  const res = await fetch(path, {
    method: "POST",
    headers: {
      Accept: "text/event-stream",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    signal,
  })

  if (res.status === 401) {
    notifyUnauthorized()
    throw new Error("未登录或会话已过期")
  }
  if (!res.ok) {
    const body = await res.text().catch(() => "")
    throw new Error(`${path} 请求失败 (${res.status}): ${body || res.statusText}`)
  }
  if (!res.body) {
    throw new Error("浏览器不支持流式响应")
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder("utf-8")
  let buf = ""

  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) {
        if (buf.trim()) flushBlock(buf, onEvent)
        return
      }
      buf += decoder.decode(value, { stream: true })
      let idx
      while ((idx = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, idx)
        buf = buf.slice(idx + 2)
        flushBlock(block, onEvent)
      }
    }
  } finally {
    reader.releaseLock()
  }
}

function flushBlock(block: string, onEvent: (ev: ProgressEvent) => void) {
  const lines = block.split("\n")
  const dataLines: string[] = []
  for (const line of lines) {
    if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  if (dataLines.length === 0) return
  const payload = dataLines.join("\n")
  try {
    const ev = JSON.parse(payload) as ProgressEvent
    onEvent(ev)
  } catch {
    // 忽略不合法的帧
  }
}

/** 触发 /api/accounts/:account_id/sync（余额 + 倍率）。 */
export function syncAccountStream(accountID: number, options: SyncOptions) {
  return streamSSE(`/api/accounts/${accountID}/sync`, options)
}

/** 触发 /api/accounts/sync-all（按站点完整同步余额、消费、订阅和倍率）。 */
export function syncAllAccountsStream(options: SyncOptions) {
  return streamSSE("/api/accounts/sync-all", options)
}

/** 触发站点内全部账号的完整同步，并逐账号返回进度。 */
export function syncSiteStream(siteID: number, options: SyncOptions) {
  return streamSSE(`/api/sites/${siteID}/sync-stream`, options)
}

/** 触发 /api/accounts/:account_id/test-login（仅登录验证，session 落库以便后续 sync 复用）。 */
export function testAccountLoginStream(accountID: number, options: SyncOptions) {
  return streamSSE(`/api/accounts/${accountID}/test-login`, options)
}

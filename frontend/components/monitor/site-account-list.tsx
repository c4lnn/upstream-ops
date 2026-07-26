"use client"

import { Fragment, useEffect, useMemo, useRef, useState } from "react"
import {
  CheckCircle2,
  ChevronDown,
  CircleAlert,
  ExternalLink,
  KeyRound,
  LogIn,
  MoreHorizontal,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  XCircle,
} from "lucide-react"
import { toast } from "sonner"
import { Card } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useConfirm } from "@/components/ui/confirm-dialog"
import { apiFetch } from "@/lib/api"
import { formatRatio, money, relativeTime } from "@/lib/format"
import {
  useAccounts,
  useAccountRates,
  useSites,
} from "@/lib/queries"
import { useTriggerRefresh } from "@/lib/refresh-context"
import {
  syncAccountStream,
  syncAllAccountsStream,
  testAccountLoginStream,
  type ProgressEvent,
} from "@/lib/sync-stream"
import { cn } from "@/lib/utils"
import type {
  AccountRedeemResult,
  UpstreamAccount,
  UpstreamSite,
} from "@/lib/api-types"
import { AccountFormDialog } from "@/components/monitor/account-form-dialog"
import { AccountDeleteDialog } from "@/components/monitor/account-delete-dialog"
import { SiteFormDialog } from "@/components/monitor/site-form-dialog"
import { SiteAccountBundleActions } from "@/components/monitor/site-account-bundle-actions"
import { AccountAPIKeysDialog } from "@/components/monitor/account-api-keys-dialog"
import { AccountRechargeDialog } from "@/components/monitor/account-recharge-dialog"
import { AccountRedeemDialog } from "@/components/monitor/account-redeem-dialog"
import { AccountSubscriptionUsageMetricTiles } from "@/components/monitor/account-subscription-usage-dialog"

type AccountStatus = "healthy" | "low" | "failed" | "idle"

function statusOf(account: UpstreamAccount): AccountStatus {
  if (account.last_error) return "failed"
  if (account.last_balance == null) return "idle"
  if (account.balance_threshold > 0 && account.last_balance < account.balance_threshold) return "low"
  return "healthy"
}

const statusStyle: Record<AccountStatus, { label: string; className: string }> = {
  healthy: { label: "健康", className: "bg-success/10 text-success ring-success/20" },
  low: { label: "低余额", className: "bg-warning/10 text-warning ring-warning/20" },
  failed: { label: "异常", className: "bg-danger/10 text-danger ring-danger/20" },
  idle: { label: "未采集", className: "bg-muted text-muted-foreground ring-border" },
}

function StatTile({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex h-16 min-w-0 flex-col justify-between rounded-md border border-border bg-muted/20 px-2.5 py-2">
      <span className="text-[10px] leading-none text-muted-foreground">{label}</span>
      <div className="min-w-0 overflow-hidden text-[13px] font-semibold leading-tight text-foreground">
        {typeof children === "string" ? <span className="block truncate">{children}</span> : children}
      </div>
    </div>
  )
}

function ratioTone(ratio: number): string {
  if (ratio <= 0.8) return "bg-success/10 text-success ring-success/20"
  if (ratio > 2) return "bg-danger/10 text-danger ring-danger/20"
  if (ratio > 1.2) return "bg-warning/10 text-warning ring-warning/20"
  return "bg-muted text-foreground ring-border"
}

interface AccountSyncState {
  running: boolean
  latest: ProgressEvent | null
  failed: boolean
}

function AccountSyncNotice({ state }: { state?: AccountSyncState }) {
  if (!state?.latest) return null
  const error = state.failed || state.latest.stage === "error"
  return (
    <div className={cn("mt-3 flex items-start gap-2 rounded-md border px-2.5 py-2 text-xs", error ? "border-danger/30 bg-danger/5 text-danger" : "border-border bg-muted/30 text-muted-foreground")}>
      {state.running ? <RefreshCw className="mt-0.5 size-3.5 shrink-0 animate-spin" /> : error ? <XCircle className="mt-0.5 size-3.5 shrink-0" /> : <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-success" />}
      <span className="min-w-0 break-words">{state.latest.message}</span>
    </div>
  )
}

function AccountRateChips({ accountID }: { accountID: number }) {
  const { data, loading } = useAccountRates(accountID)
  const rates = [...(data ?? [])].sort((left, right) => left.ratio - right.ratio)
  const [expanded, setExpanded] = useState(false)
  const [hasOverflow, setHasOverflow] = useState(false)
  const chipBoxRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const element = chipBoxRef.current
    if (!element) return
    const check = () => {
      if (expanded) return
      setHasOverflow(element.scrollHeight > element.clientHeight + 1)
    }
    check()
    const observer = new ResizeObserver(check)
    observer.observe(element)
    return () => observer.disconnect()
  }, [rates.length, expanded])

  if (loading || rates.length === 0) return null

  const showToggle = hasOverflow || expanded

  return (
    <div className="mt-3 border-t border-border pt-2.5">
      <div className="mb-1.5 flex items-center justify-between">
        <p className="text-[11px] text-muted-foreground">{rates.length} 个倍率分组</p>
        {showToggle ? (
          <button
            type="button"
            onClick={() => setExpanded((value) => !value)}
            className="inline-flex items-center gap-0.5 text-[11px] text-muted-foreground hover:text-foreground"
          >
            {expanded ? "收起" : "展开"}
            <ChevronDown className={cn("size-3 transition-transform duration-200", expanded && "rotate-180")} />
          </button>
        ) : null}
      </div>

      <div className="relative min-h-16">
        <div
          ref={chipBoxRef}
          className={cn(
            "flex flex-wrap gap-1 overflow-hidden transition-[max-height] duration-300 ease-out",
            expanded ? "max-h-150" : "max-h-12",
          )}
        >
          {rates.map((rate) => (
            <Tooltip key={rate.id} delayDuration={150}>
              <TooltipTrigger asChild>
                <span
                  className={cn(
                    "inline-flex max-w-full cursor-default items-center gap-1 rounded px-1.5 py-0.5 text-[11px] ring-1 ring-inset transition-colors hover:bg-muted/60",
                    ratioTone(rate.ratio),
                  )}
                >
                  <span className="max-w-36 truncate font-medium">{rate.model_name}</span>
                  <span className="rounded bg-primary/10 px-1 font-semibold tabular-nums text-primary ring-1 ring-inset ring-primary/15">{formatRatio(rate.ratio)}</span>
                </span>
              </TooltipTrigger>
              <TooltipContent side="top" className="max-w-xs text-xs">
                <p className="font-medium">{rate.model_name}</p>
                {rate.description ? (
                  <p className="mt-0.5 text-muted-foreground">{rate.description}</p>
                ) : (
                  <p className="mt-0.5 italic text-muted-foreground">{"(无描述)"}</p>
                )}
                <p className="mt-0.5 text-muted-foreground">最近更新：{relativeTime(rate.last_seen_at)}</p>
              </TooltipContent>
            </Tooltip>
          ))}
        </div>
        {!expanded && hasOverflow ? (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-4 bg-linear-to-t from-background to-transparent" />
        ) : null}
      </div>
    </div>
  )
}

function SiteHeader({
  site,
  collapsed,
  onToggle,
  onCreateAccount,
  onEdit,
  onDelete,
  onSync,
  syncing,
}: {
  site: UpstreamSite
  collapsed: boolean
  onToggle: () => void
  onCreateAccount: () => void
  onEdit: () => void
  onDelete: () => void
  onSync: () => void
  syncing: boolean
}) {
  return (
    <div className="col-span-full border-y border-border bg-muted/10 px-3 py-3">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <button type="button" className="flex min-w-0 items-center gap-2 text-left" onClick={onToggle}>
          <ChevronDown className={cn("size-4 shrink-0 text-muted-foreground transition-transform", collapsed && "-rotate-90")} />
          <span className="truncate text-sm font-semibold text-foreground">{site.name}</span>
          <span className="shrink-0 text-xs text-muted-foreground">{site.account_count} 个账号</span>
        </button>
        <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span className="rounded bg-background px-1.5 py-0.5 font-medium text-foreground ring-1 ring-inset ring-border">
            {site.type === "newapi" ? "NewAPI" : "Sub2API"}
          </span>
          <a
            href={site.base_url}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex min-w-0 items-center gap-1 text-brand hover:underline"
            title={site.base_url}
          >
            <span className="max-w-56 truncate">{site.base_url}</span>
            <ExternalLink className="size-3 shrink-0" />
          </a>
          <span>总余额 <b className="font-medium text-foreground">{money(site.total_balance)}</b></span>
          <span>今日消费 <b className="font-medium text-foreground">{money(site.today_cost)}</b></span>
          {site.error_account_count > 0 ? <span className="text-danger">异常 {site.error_account_count}</span> : null}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon-sm" title="新增账号" onClick={onCreateAccount}><Plus className="size-3.5" /></Button></TooltipTrigger><TooltipContent>新增账号</TooltipContent></Tooltip>
          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon-sm" title="同步站点账号" disabled={syncing} onClick={onSync}><RefreshCw className={cn("size-3.5", syncing && "animate-spin")} /></Button></TooltipTrigger><TooltipContent>同步站点账号</TooltipContent></Tooltip>
          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon-sm" title="编辑站点" onClick={onEdit}><Pencil className="size-3.5" /></Button></TooltipTrigger><TooltipContent>编辑站点</TooltipContent></Tooltip>
          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon-sm" title="删除站点" className="text-muted-foreground hover:text-destructive" onClick={onDelete}><Trash2 className="size-3.5" /></Button></TooltipTrigger><TooltipContent>删除站点</TooltipContent></Tooltip>
        </div>
      </div>
    </div>
  )
}

export function SiteAccountList() {
  const accountsQuery = useAccounts()
  const sitesQuery = useSites()
  const refresh = useTriggerRefresh()
  const { confirm, dialog: confirmDialog } = useConfirm()
  const [collapsed, setCollapsed] = useState<Set<number>>(new Set())
  const [editingAccount, setEditingAccount] = useState<UpstreamAccount | null>(null)
  const [accountSite, setAccountSite] = useState<UpstreamSite | null>(null)
  const [creatingAccountFor, setCreatingAccountFor] = useState<UpstreamSite | null>(null)
  const [editingSite, setEditingSite] = useState<UpstreamSite | null>(null)
  const [creatingSite, setCreatingSite] = useState(false)
  const [deletingAccount, setDeletingAccount] = useState<{ account: UpstreamAccount; site: UpstreamSite } | null>(null)
  const [redeeming, setRedeeming] = useState<UpstreamAccount | null>(null)
  const [recharging, setRecharging] = useState<UpstreamAccount | null>(null)
  const [managingKeys, setManagingKeys] = useState<UpstreamAccount | null>(null)
  const [syncState, setSyncState] = useState<Record<number, AccountSyncState>>({})
  const [bulkSyncing, setBulkSyncing] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)
  const timers = useRef<Map<number, ReturnType<typeof setTimeout>>>(new Map())

  const accounts = accountsQuery.data ?? []
  const sites = sitesQuery.data ?? []
  const siteByID = useMemo(() => new Map(sites.map((site) => [site.id, site])), [sites])
  const groups = useMemo(() => {
    const bySite = new Map<number, UpstreamAccount[]>()
    for (const account of accounts) {
      const list = bySite.get(account.site_id) ?? []
      list.push(account)
      bySite.set(account.site_id, list)
    }
    return sites.map((site) => ({ site, accounts: bySite.get(site.id) ?? [] }))
  }, [accounts, sites])
  const total = accounts.length
  const anySyncing = bulkSyncing || Object.values(syncState).some((state) => state.running)

  useEffect(() => () => timers.current.forEach((timer) => clearTimeout(timer)), [])

  function updateSync(accountID: number, next: AccountSyncState) {
    setSyncState((current) => ({ ...current, [accountID]: next }))
  }

  function dismissSync(accountID: number) {
    const timer = timers.current.get(accountID)
    if (timer) clearTimeout(timer)
    timers.current.set(accountID, setTimeout(() => {
      setSyncState((current) => {
        const next = { ...current }
        delete next[accountID]
        return next
      })
    }, 5000))
  }

  async function runAccountAction(account: UpstreamAccount, action: "sync" | "login") {
    updateSync(account.id, { running: true, latest: { stage: "session", message: "准备中", time: new Date().toISOString() }, failed: false })
    let failed = false
    try {
      await (action === "sync" ? syncAccountStream : testAccountLoginStream)(account.id, {
        onEvent: (event) => {
          failed ||= event.stage === "error" || event.ok === false
          updateSync(account.id, { running: event.stage !== "done" && event.stage !== "error", latest: event, failed })
        },
      })
      dismissSync(account.id)
    } catch (reason) {
      failed = true
      updateSync(account.id, { running: false, failed, latest: { stage: "error", message: (reason as Error).message || "操作失败", time: new Date().toISOString() } })
    } finally {
      refresh()
    }
  }

  async function runAllAccounts() {
    if (accounts.length === 0) return
    setBulkSyncing(true)
    try {
      await syncAllAccountsStream({
        onEvent: (event) => {
          if (event.account_id == null) return
          const failed = event.stage === "error" || event.ok === false
          updateSync(event.account_id, { running: event.stage !== "done" && event.stage !== "error", latest: event, failed })
          if (event.stage === "done") dismissSync(event.account_id)
        },
      })
    } catch (reason) {
      toast.error((reason as Error).message || "同步失败")
    } finally {
      setBulkSyncing(false)
      refresh()
    }
  }

  async function withBusy(key: string, action: () => Promise<unknown>) {
    setBusy(key)
    try {
      await action()
      refresh()
    } catch (reason) {
      toast.error((reason as Error).message || "操作失败")
    } finally {
      setBusy(null)
    }
  }

  async function deleteSite(site: UpstreamSite) {
    const ok = await confirm({
      title: `删除站点 ${site.name}？`,
      description: `将删除 ${site.account_count} 个账号及其运行历史，且无法恢复。`,
      confirmLabel: "删除",
      destructive: true,
    })
    if (ok) void withBusy(`delete-site-${site.id}`, () => apiFetch(`/sites/${site.id}?cascade=true`, { method: "DELETE" }))
  }

  function redeemSummary(result: AccountRedeemResult) {
    if (result.type === "subscription") return `${result.message || "兑换成功"}${result.group_name ? ` · ${result.group_name}` : ""}`
    if (result.type === "concurrency") return `${result.message || "兑换成功"}${result.new_concurrency != null ? ` · 当前并发 ${result.new_concurrency}` : ""}`
    return `${result.message || "兑换成功"}${result.new_balance != null ? ` · 当前余额 ${money(result.new_balance)}` : ""}`
  }

  const loading = (accountsQuery.loading && !accountsQuery.data) || (sitesQuery.loading && !sitesQuery.data)

  return (
    <section>
      <div className="mb-3 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-baseline gap-3">
          <h2 className="text-base font-semibold text-foreground">站点与账号</h2>
          <p className="text-xs text-muted-foreground">{sites.length} 个站点 · {total} 个账号</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <SiteAccountBundleActions sites={sites} onImported={refresh} />
          <Button variant="outline" size="sm" className="gap-1.5 text-xs" disabled={anySyncing || accounts.length === 0} onClick={() => void runAllAccounts()}>
            <RefreshCw className={cn("size-3.5", bulkSyncing && "animate-spin")} />同步全部
          </Button>
          <Button size="sm" className="gap-1.5 text-xs" onClick={() => setCreatingSite(true)}><Plus className="size-3.5" />新增站点</Button>
        </div>
      </div>

      {loading ? (
        <p className="rounded-md border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">加载中...</p>
      ) : sites.length === 0 ? (
        <div className="rounded-md border border-dashed border-border px-4 py-10 text-center">
          <p className="text-sm text-muted-foreground">还没有站点。</p>
          <Button size="sm" className="mt-3 gap-1.5" onClick={() => setCreatingSite(true)}><Plus className="size-3.5" />新增第一个站点</Button>
        </div>
      ) : total === 0 ? (
        <div className="space-y-3">
          {sites.map((site) => <SiteHeader key={site.id} site={site} collapsed={false} onToggle={() => undefined} onCreateAccount={() => setCreatingAccountFor(site)} onEdit={() => setEditingSite(site)} onDelete={() => void deleteSite(site)} onSync={() => undefined} syncing={false} />)}
          <p className="rounded-md border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">站点已创建，新增账号后即可登录和同步。</p>
        </div>
      ) : (
        <>
          <div className="grid grid-cols-1 items-start gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {groups.map(({ site, accounts: siteAccounts }) => (
              <Fragment key={site.id}>
                <SiteHeader
                  site={site}
                  collapsed={collapsed.has(site.id)}
                  onToggle={() => setCollapsed((current) => {
                    const next = new Set(current)
                    if (next.has(site.id)) next.delete(site.id)
                    else next.add(site.id)
                    return next
                  })}
                  onCreateAccount={() => setCreatingAccountFor(site)}
                  onEdit={() => setEditingSite(site)}
                  onDelete={() => void deleteSite(site)}
                  syncing={busy === `sync-site-${site.id}`}
                  onSync={() => void withBusy(`sync-site-${site.id}`, async () => { await apiFetch(`/sites/${site.id}/sync`, { method: "POST" }); toast.success(`${site.name} 同步完成`) })}
                />
                {!collapsed.has(site.id) && site.account_count === 0 ? (
                  <p className="col-span-full rounded-md border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">站点已创建，新增账号后即可登录和同步。</p>
                ) : null}
                {collapsed.has(site.id) ? null : siteAccounts.map((account) => {
                  const status = statusStyle[statusOf(account)]
                  return (
                    <Card key={account.id} className="flex flex-col border border-border p-3 shadow-none sm:p-4">
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <h3 className="truncate text-sm font-semibold text-foreground">{account.alias}</h3>
                            {site.default_account_id === account.id ? <span className="rounded bg-success/10 px-1.5 py-0.5 text-[10px] font-medium text-success ring-1 ring-inset ring-success/20">默认账号</span> : null}
                            {!account.monitor_enabled ? <span className="rounded bg-warning/10 px-1.5 py-0.5 text-[10px] font-medium text-warning ring-1 ring-inset ring-warning/20">监控已暂停</span> : null}
                          </div>
                          <p className="mt-1 truncate text-xs text-muted-foreground">{account.username || "Token 凭据"}</p>
                        </div>
                      </div>
                      <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-3">
                        <StatTile label="余额">{money(account.last_balance)}</StatTile>
                        <StatTile label="今日消费">{money(account.today_cost)}</StatTile>
                        <StatTile label="阈值 / 状态">
                          <div className="flex min-w-0 items-center gap-1.5">
                            <Tooltip delayDuration={150}>
                              <TooltipTrigger asChild>
                                <span className="truncate text-[11px] font-medium text-foreground">
                                  {account.balance_threshold > 0 ? money(account.balance_threshold) : "未设置"}
                                </span>
                              </TooltipTrigger>
                              <TooltipContent side="top" className="text-xs">
                                {account.balance_threshold > 0
                                  ? `余额低于 ${money(account.balance_threshold)} 时通知`
                                  : "未开启低余额通知"}
                              </TooltipContent>
                            </Tooltip>
                            <span className="text-[10px] text-muted-foreground">/</span>
                            <span className={cn("inline-flex shrink-0 items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset", status.className)}>
                              {status.label}
                            </span>
                          </div>
                        </StatTile>
                      </div>
                      {account.last_error ? <div className="mt-2 flex gap-1.5 text-xs text-danger"><CircleAlert className="mt-0.5 size-3.5 shrink-0" /><span className="min-w-0 break-words">{account.last_error}</span></div> : null}
                      <AccountSyncNotice state={syncState[account.id]} />
                      <AccountRateChips accountID={account.id} />
                      {account.subscription_enabled ? <AccountSubscriptionUsageMetricTiles account={account} /> : null}
                      <div className="mt-3 flex items-center justify-between gap-2 border-t border-border pt-3">
                        <span className="text-[11px] text-muted-foreground">{relativeTime(account.last_balance_at ?? account.updated_at)}</span>
                        <div className="flex items-center gap-1">
                          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon-sm" title="同步账号" disabled={syncState[account.id]?.running} onClick={() => void runAccountAction(account, "sync")}><RefreshCw className={cn("size-3.5", syncState[account.id]?.running && "animate-spin")} /></Button></TooltipTrigger><TooltipContent>同步账号</TooltipContent></Tooltip>
                          <Tooltip><TooltipTrigger asChild><Button variant="ghost" size="icon-sm" title="测试登录" disabled={syncState[account.id]?.running} onClick={() => void runAccountAction(account, "login")}><LogIn className="size-3.5" /></Button></TooltipTrigger><TooltipContent>测试登录</TooltipContent></Tooltip>
                          <DropdownMenu>
                            <DropdownMenuTrigger asChild><Button variant="ghost" size="icon-sm" aria-label="账号操作"><MoreHorizontal className="size-3.5" /></Button></DropdownMenuTrigger>
                            <DropdownMenuContent align="end" className="w-44">
                              <DropdownMenuItem onSelect={(event) => { event.preventDefault(); setEditingAccount(account); setAccountSite(site) }}><Pencil className="size-3.5" />编辑账号</DropdownMenuItem>
                              {site.default_account_id !== account.id ? <DropdownMenuItem onSelect={(event) => { event.preventDefault(); void withBusy(`default-${account.id}`, async () => { await apiFetch(`/sites/${site.id}/default-account`, { method: "POST", body: JSON.stringify({ account_id: account.id }) }); toast.success(`${account.alias} 已设为默认账号`) }) }}><CheckCircle2 className="size-3.5" />设为默认账号</DropdownMenuItem> : null}
                              <DropdownMenuItem onSelect={(event) => { event.preventDefault(); void withBusy(`monitor-${account.id}`, () => apiFetch(`/accounts/${account.id}/${account.monitor_enabled ? "disable" : "enable"}`, { method: "POST" })) }}><Play className="size-3.5" />{account.monitor_enabled ? "暂停监控" : "恢复监控"}</DropdownMenuItem>
                              <DropdownMenuItem onSelect={(event) => { event.preventDefault(); void withBusy(`clear-${account.id}`, () => apiFetch(`/accounts/${account.id}/clear-login-info`, { method: "POST" })) }}><XCircle className="size-3.5" />清空登录信息</DropdownMenuItem>
                              <DropdownMenuItem onSelect={(event) => { event.preventDefault(); setManagingKeys(account) }}><KeyRound className="size-3.5" />API Keys</DropdownMenuItem>
                              <DropdownMenuItem onSelect={(event) => { event.preventDefault(); setRecharging(account) }}>充值</DropdownMenuItem>
                              <DropdownMenuItem onSelect={(event) => { event.preventDefault(); setRedeeming(account) }}>兑换</DropdownMenuItem>
                              <DropdownMenuSeparator />
                              <DropdownMenuItem variant="destructive" onSelect={(event) => { event.preventDefault(); setDeletingAccount({ account, site }) }}><Trash2 className="size-3.5" />删除账号</DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        </div>
                      </div>
                    </Card>
                  )
                })}
              </Fragment>
            ))}
          </div>
        </>
      )}

      <AccountFormDialog account={editingAccount} site={editingAccount ? accountSite : creatingAccountFor} open={editingAccount != null || creatingAccountFor != null} onOpenChange={(open) => { if (!open) { setEditingAccount(null); setAccountSite(null); setCreatingAccountFor(null) } }} />
      <SiteFormDialog site={editingSite} open={creatingSite || editingSite != null} onOpenChange={(open) => { if (!open) { setCreatingSite(false); setEditingSite(null) } }} />
      <AccountDeleteDialog account={deletingAccount?.account ?? null} site={deletingAccount?.site ?? null} open={deletingAccount != null} onOpenChange={(open) => { if (!open) setDeletingAccount(null) }} />
      <AccountRedeemDialog account={redeeming} open={redeeming != null} onOpenChange={(open) => { if (!open) setRedeeming(null) }} onSuccess={(result) => toast.success(redeemSummary(result))} />
      <AccountRechargeDialog account={recharging} open={recharging != null} onOpenChange={(open) => { if (!open) setRecharging(null) }} />
      <AccountAPIKeysDialog account={managingKeys} site={managingKeys ? siteByID.get(managingKeys.site_id) ?? null : null} open={managingKeys != null} onOpenChange={(open) => { if (!open) setManagingKeys(null) }} />
      {confirmDialog}
    </section>
  )
}

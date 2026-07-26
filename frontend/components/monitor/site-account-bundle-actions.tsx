"use client"

import { useEffect, useMemo, useState } from "react"
import {
  AlertTriangle,
  CheckCircle2,
  Download,
  FileJson,
  Loader2,
  ShieldCheck,
  Upload,
} from "lucide-react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  commitSiteAccountBundle,
  downloadSiteAccountBundle,
  previewSiteAccountBundle,
} from "@/lib/site-account-bundles"
import type {
  SiteAccountBundleImportPlan,
  SiteAccountBundleImportResult,
  SiteAccountBundleImportStrategy,
  SiteAccountBundlePlanAction,
  UpstreamSite,
} from "@/lib/api-types"
import { cn } from "@/lib/utils"

interface SiteAccountBundleActionsProps {
  sites: UpstreamSite[]
  onImported: () => void
}

export function SiteAccountBundleActions({ sites, onImported }: SiteAccountBundleActionsProps) {
  const [exportOpen, setExportOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)

  return (
    <>
      <Tooltip delayDuration={200}>
        <TooltipTrigger asChild>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            aria-label="导出站点账号配置"
            disabled={sites.length === 0}
            onClick={() => setExportOpen(true)}
          >
            <Download className="size-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-xs">导出站点账号配置</TooltipContent>
      </Tooltip>
      <Tooltip delayDuration={200}>
        <TooltipTrigger asChild>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            aria-label="导入站点账号配置"
            onClick={() => setImportOpen(true)}
          >
            <Upload className="size-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-xs">导入站点账号配置</TooltipContent>
      </Tooltip>

      <ExportDialog sites={sites} open={exportOpen} onOpenChange={setExportOpen} />
      <ImportDialog open={importOpen} onOpenChange={setImportOpen} onImported={onImported} />
    </>
  )
}

function ExportDialog({
  sites,
  open,
  onOpenChange,
}: {
  sites: UpstreamSite[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [includeCredentials, setIncludeCredentials] = useState(false)
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [busy, setBusy] = useState(false)
  const siteIDKey = sites.map((site) => site.id).join(",")
  const allSelected = sites.length > 0 && selected.size === sites.length
  const passwordMismatch = includeCredentials && password !== confirmPassword

  useEffect(() => {
    if (!open) return
    setSelected(new Set(siteIDKey.split(",").filter(Boolean).map(Number)))
    setIncludeCredentials(false)
    setPassword("")
    setConfirmPassword("")
  }, [open, siteIDKey])

  function handleOpenChange(next: boolean) {
    onOpenChange(next)
  }

  async function handleExport() {
    if (selected.size === 0) {
      toast.error("至少选择一个站点")
      return
    }
    if (includeCredentials && !password.trim()) {
      toast.error("请输入导出密码")
      return
    }
    if (passwordMismatch) {
      toast.error("两次输入的导出密码不一致")
      return
    }
    setBusy(true)
    try {
      await downloadSiteAccountBundle({
        siteIDs: [...selected],
        includeCredentials,
        password: includeCredentials ? password : undefined,
      })
      toast.success(`已导出 ${selected.size} 个站点`)
      onOpenChange(false)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "导出失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="flex max-h-[88vh] max-w-xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>导出站点账号配置</DialogTitle>
          <DialogDescription>选择需要迁移的站点及凭据保护方式。</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 space-y-4 overflow-y-auto pr-1">
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <Label>站点</Label>
              <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
                <Checkbox
                  checked={allSelected ? true : selected.size > 0 ? "indeterminate" : false}
                  onCheckedChange={(checked) => {
                    setSelected(checked ? new Set(sites.map((site) => site.id)) : new Set())
                  }}
                />
                全选
              </label>
            </div>
            <div className="max-h-56 divide-y divide-border overflow-y-auto rounded-md border border-border">
              {sites.map((site) => (
                <label key={site.id} className="flex cursor-pointer items-center gap-3 px-3 py-2.5 hover:bg-muted/40">
                  <Checkbox
                    checked={selected.has(site.id)}
                    onCheckedChange={(checked) => {
                      setSelected((current) => {
                        const next = new Set(current)
                        if (checked) next.add(site.id)
                        else next.delete(site.id)
                        return next
                      })
                    }}
                  />
                  <span className="min-w-0 flex-1 truncate text-sm">{site.name}</span>
                  <span className="shrink-0 text-xs text-muted-foreground">{site.account_count} 个账号</span>
                </label>
              ))}
            </div>
          </div>

          <div className="flex items-center justify-between gap-4 border-t border-border pt-4">
            <div className="min-w-0">
              <p className="text-sm font-medium">包含账号凭据</p>
              <p className="text-xs text-muted-foreground">密码和 token 将使用导出密码重新加密。</p>
            </div>
            <Switch
              checked={includeCredentials}
              onCheckedChange={setIncludeCredentials}
              aria-label="包含账号凭据"
            />
          </div>

          {includeCredentials ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="bundle-export-password">导出密码</Label>
                <Input
                  id="bundle-export-password"
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="bundle-export-password-confirm">确认密码</Label>
                <Input
                  id="bundle-export-password-confirm"
                  type="password"
                  autoComplete="new-password"
                  value={confirmPassword}
                  onChange={(event) => setConfirmPassword(event.target.value)}
                  aria-invalid={passwordMismatch}
                />
                {passwordMismatch ? <p className="text-xs text-danger">两次密码不一致</p> : null}
              </div>
            </div>
          ) : null}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>取消</Button>
          <Button
            onClick={() => void handleExport()}
            disabled={busy || selected.size === 0 || passwordMismatch || (includeCredentials && !password.trim())}
          >
            {busy ? <Loader2 className="size-4 animate-spin" /> : <Download className="size-4" />}
            导出
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ImportDialog({
  open,
  onOpenChange,
  onImported,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onImported: () => void
}) {
  const [file, setFile] = useState<File | null>(null)
  const [protectedBundle, setProtectedBundle] = useState(false)
  const [strategy, setStrategy] = useState<SiteAccountBundleImportStrategy>("create_only")
  const [password, setPassword] = useState("")
  const [plan, setPlan] = useState<SiteAccountBundleImportPlan | null>(null)
  const [result, setResult] = useState<SiteAccountBundleImportResult | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const [committing, setCommitting] = useState(false)
  const [confirmBaseURLChanges, setConfirmBaseURLChanges] = useState(false)

  const visibleItems = useMemo(() => plan?.items ?? result?.items ?? [], [plan, result])

  function reset() {
    setFile(null)
    setProtectedBundle(false)
    setStrategy("create_only")
    setPassword("")
    setPlan(null)
    setResult(null)
    setPreviewing(false)
    setCommitting(false)
    setConfirmBaseURLChanges(false)
  }

  function handleOpenChange(next: boolean) {
    if (!next) reset()
    onOpenChange(next)
  }

  async function handleFile(nextFile: File | null) {
    setFile(nextFile)
    setPlan(null)
    setResult(null)
    setProtectedBundle(false)
    setConfirmBaseURLChanges(false)
    if (!nextFile) return
    try {
      const parsed = JSON.parse(await nextFile.text()) as { credentials?: unknown }
      setProtectedBundle(Boolean(parsed.credentials))
    } catch {
      // Backend preview returns the authoritative parse error.
    }
  }

  async function handlePreview() {
    if (!file) {
      toast.error("请选择站点账号配置包")
      return
    }
    if (protectedBundle && !password) {
      toast.error("请输入导出密码")
      return
    }
    setPreviewing(true)
    setResult(null)
    setConfirmBaseURLChanges(false)
    try {
      setPlan(await previewSiteAccountBundle(file, strategy, password, false))
    } catch (error) {
      setPlan(null)
      toast.error(error instanceof Error ? error.message : "预检失败")
    } finally {
      setPreviewing(false)
    }
  }

  async function handleCommit() {
    if (!file || !plan) return
    setCommitting(true)
    try {
      const imported = await commitSiteAccountBundle(file, strategy, password, plan.digest, confirmBaseURLChanges)
      setResult(imported)
      setPlan(null)
      onImported()
      toast.success(`导入完成：新增 ${imported.summary.create}，更新 ${imported.summary.update}`)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "导入失败")
    } finally {
      setCommitting(false)
    }
  }

	const summary = plan?.summary ?? result?.summary
	const hasNonConfirmableConflict = Boolean(
		plan?.items.some(
			(item) =>
				item.action === "conflict" &&
				!item.changes?.some((change) => change.field === "base_url"),
		),
	)

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="flex max-h-[90vh] max-w-3xl flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>导入站点账号配置</DialogTitle>
          <DialogDescription>仅支持新的站点账号配置包；旧渠道配置包不会转换。预检不会写入数据。</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 space-y-4 overflow-y-auto pr-1">
          <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
            <div className="space-y-1.5">
              <Label htmlFor="bundle-import-file">配置包</Label>
              <Input
                id="bundle-import-file"
                type="file"
                accept="application/json,.json"
                onChange={(event) => void handleFile(event.target.files?.[0] ?? null)}
              />
            </div>
            {file ? (
              <Badge variant="outline" className="h-9 max-w-full gap-1.5 px-3">
                {protectedBundle ? <ShieldCheck className="size-3.5" /> : <FileJson className="size-3.5" />}
                <span className="max-w-48 truncate">{protectedBundle ? "受密码保护" : "脱敏配置包"}</span>
              </Badge>
            ) : null}
          </div>

          <div className="space-y-1.5">
            <Label>导入策略</Label>
            <div className="grid grid-cols-2 rounded-md border border-border p-1">
              <Button
                type="button"
                size="sm"
                variant={strategy === "create_only" ? "default" : "ghost"}
                aria-pressed={strategy === "create_only"}
                onClick={() => { setStrategy("create_only"); setPlan(null); setResult(null) }}
              >
                仅新增
              </Button>
              <Button
                type="button"
                size="sm"
                variant={strategy === "upsert" ? "default" : "ghost"}
                aria-pressed={strategy === "upsert"}
                onClick={() => { setStrategy("upsert"); setPlan(null); setResult(null) }}
              >
                更新或新增
              </Button>
            </div>
          </div>

          {protectedBundle ? (
            <div className="space-y-1.5">
              <Label htmlFor="bundle-import-password">导出密码</Label>
              <Input
                id="bundle-import-password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => { setPassword(event.target.value); setPlan(null); setResult(null) }}
              />
            </div>
          ) : null}

          {summary ? <PlanSummary summary={summary} completed={Boolean(result)} /> : null}

          {plan?.requires_base_url_confirmation ? (
            <div className="space-y-3 rounded-md border border-warning/40 bg-warning/5 px-3 py-3">
              <div className="flex items-start gap-2 text-sm font-medium text-foreground">
                <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
                <span>导入将修改站点入口地址</span>
              </div>
              <div className="space-y-1.5 text-xs text-muted-foreground">
                {plan.base_url_changes.map((change) => (
                  <p key={change.site_key} className="break-words">
                    <span className="font-medium text-foreground">{change.site_name}</span>
                    {`：${change.before} -> ${change.after}（${change.affected_account_count} 个账号）`}
                  </p>
                ))}
              </div>
              <p className="text-xs text-warning">确认提交后，受影响账号的登录会话会被清除，监控会暂停；系统不会自动向新地址发送凭据。</p>
              <label className="flex cursor-pointer items-start gap-2 text-xs text-foreground">
                <Checkbox
                  checked={confirmBaseURLChanges}
                  onCheckedChange={(checked) => setConfirmBaseURLChanges(Boolean(checked))}
                />
                <span>我了解上述影响，并确认修改这些站点入口地址</span>
              </label>
            </div>
          ) : null}

          {(plan?.warnings ?? result?.warnings)?.length ? (
            <div className="space-y-1.5 rounded-md border border-warning/30 bg-warning/5 px-3 py-2.5">
              {(plan?.warnings ?? result?.warnings ?? []).map((warning) => (
                <p key={warning} className="flex items-start gap-1.5 break-words text-xs text-warning">
                  <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                  <span className="min-w-0">{warning}</span>
                </p>
              ))}
            </div>
          ) : null}

          {visibleItems.length > 0 ? (
            <div className="divide-y divide-border rounded-md border border-border">
              {visibleItems.map((item) => (
                <div key={`${item.scope}:${item.key}`} className="space-y-2 px-3 py-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <ActionBadge action={item.action} />
                    <span className="min-w-0 flex-1 break-words text-sm font-medium">{item.name}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{item.scope === "site" ? "站点" : "账号"}</span>
                  </div>
                  {item.message ? <p className="text-xs text-muted-foreground">{item.message}</p> : null}
                  {item.changes?.length ? (
                    <ul className="space-y-1 text-xs text-muted-foreground">
                      {item.changes.map((change) => (
                        <li key={change.field} className="grid gap-1 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-2">
                          <span className="break-words font-medium text-foreground/80">{change.field}</span>
                          <span className="min-w-0 break-all">{formatChange(change.before)} → {formatChange(change.after)}</span>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                  {item.needs_credential ? (
                    <p className="flex items-start gap-1.5 text-xs text-warning">
                      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />需要补充凭据，监控保持关闭
                    </p>
                  ) : null}
                  {item.warnings?.map((warning) => (
                    <p key={warning} className="flex items-start gap-1.5 text-xs text-warning">
                      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />{warning}
                    </p>
                  ))}
                </div>
              ))}
            </div>
          ) : null}
        </div>

        <DialogFooter>
          {result ? (
            <Button onClick={() => handleOpenChange(false)}><CheckCircle2 className="size-4" />完成</Button>
          ) : (
            <>
              <Button variant="outline" onClick={() => onOpenChange(false)} disabled={previewing || committing}>取消</Button>
              <Button variant="outline" onClick={() => void handlePreview()} disabled={!file || previewing || committing}>
                {previewing ? <Loader2 className="size-4 animate-spin" /> : <FileJson className="size-4" />}
                预检
              </Button>
			  <Button onClick={() => void handleCommit()} disabled={!plan || hasNonConfirmableConflict || (plan.requires_base_url_confirmation && !confirmBaseURLChanges) || previewing || committing}>
                {committing ? <Loader2 className="size-4 animate-spin" /> : <Upload className="size-4" />}
                确认导入
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function PlanSummary({
  summary,
  completed,
}: {
  summary: SiteAccountBundleImportPlan["summary"]
  completed: boolean
}) {
  const values = [
    ["新增", summary.create, "text-success"],
    ["更新", summary.update, "text-foreground"],
    ["跳过", summary.skip, "text-muted-foreground"],
    ["冲突", summary.conflict, "text-danger"],
    ["警告", summary.warnings, "text-warning"],
    ["缺凭据", summary.need_credential, "text-warning"],
  ] as const
  return (
    <div>
      <div className="mb-2 flex items-center gap-2 text-sm font-medium">
        {completed ? <CheckCircle2 className="size-4 text-success" /> : <FileJson className="size-4" />}
        {completed ? "导入结果" : "预检结果"}
      </div>
      <div className="grid grid-cols-3 gap-px overflow-hidden rounded-md border border-border bg-border sm:grid-cols-6">
        {values.map(([label, value, tone]) => (
          <div key={label} className="bg-background px-2 py-2 text-center">
            <div className={cn("text-base font-semibold", tone)}>{value}</div>
            <div className="text-[11px] text-muted-foreground">{label}</div>
          </div>
        ))}
      </div>
    </div>
  )
}

const actionMeta: Record<SiteAccountBundlePlanAction, { label: string; className: string }> = {
  create: { label: "新增", className: "border-success/30 bg-success/10 text-success" },
  update: { label: "更新", className: "border-foreground/20 bg-muted text-foreground" },
  skip: { label: "跳过", className: "border-border bg-background text-muted-foreground" },
  conflict: { label: "冲突", className: "border-danger/30 bg-danger/10 text-danger" },
}

function ActionBadge({ action }: { action: SiteAccountBundlePlanAction }) {
  const meta = actionMeta[action]
  return <Badge variant="outline" className={meta.className}>{meta.label}</Badge>
}

function formatChange(value: unknown) {
  if (value == null || value === "") return "—"
  if (typeof value === "boolean") return value ? "开启" : "关闭"
  if (typeof value === "object") return JSON.stringify(value)
  return String(value)
}

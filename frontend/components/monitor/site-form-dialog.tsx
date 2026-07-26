"use client"

import { useEffect, useState, type FormEvent } from "react"
import { AlertTriangle } from "lucide-react"
import { apiFetch } from "@/lib/api"
import { useTriggerRefresh } from "@/lib/refresh-context"
import type { UpstreamSite, UpstreamSiteType } from "@/lib/api-types"
import { Button } from "@/components/ui/button"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"

interface SiteFormState {
  name: string
  type: UpstreamSiteType
  base_url: string
  sort_order: string
  ignore_announcements: boolean
}

function initialState(site: UpstreamSite | null): SiteFormState {
  return {
    name: site?.name ?? "",
    type: site?.type ?? "sub2api",
    base_url: site?.base_url ?? "",
    sort_order: String(site?.sort_order ?? 1),
    ignore_announcements: site?.ignore_announcements ?? false,
  }
}

export function SiteFormDialog({
  site,
  open,
  onOpenChange,
}: {
  site: UpstreamSite | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const refresh = useTriggerRefresh()
  const [form, setForm] = useState<SiteFormState>(() => initialState(site))
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmingBaseURL, setConfirmingBaseURL] = useState(false)

  useEffect(() => {
    if (!open) return
    setForm(initialState(site))
    setError(null)
    setConfirmingBaseURL(false)
  }, [open, site])

  const isEdit = Boolean(site)
  const baseURLChanged = isEdit && form.base_url.trim() !== site?.base_url

  function validate() {
    const sortOrder = Number(form.sort_order)
    if (!form.name.trim()) return "站点名称不能为空"
    if (!Number.isInteger(sortOrder)) return "排序必须是整数"
    if (!form.base_url.trim()) return "请输入站点入口地址"
    try {
      const url = new URL(form.base_url.trim())
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        return "站点入口地址只支持 HTTP 或 HTTPS"
      }
    } catch {
      return "站点入口地址格式无效"
    }
    return null
  }

  async function save(confirmBaseURLChange: boolean) {
    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }

    setSubmitting(true)
    setError(null)
    try {
      const body = {
        name: form.name.trim(),
        type: form.type,
        base_url: form.base_url.trim(),
        sort_order: Number(form.sort_order),
        ignore_announcements: form.ignore_announcements,
        ...(confirmBaseURLChange ? { confirm_base_url_change: true } : {}),
      }
      await apiFetch(isEdit ? `/sites/${site!.id}` : "/sites", {
        method: isEdit ? "PUT" : "POST",
        body: JSON.stringify(body),
      })
      refresh()
      setConfirmingBaseURL(false)
      onOpenChange(false)
    } catch (reason) {
      setError((reason as Error).message || "保存失败")
      setConfirmingBaseURL(false)
    } finally {
      setSubmitting(false)
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }
    if (baseURLChanged) {
      setConfirmingBaseURL(true)
      return
    }
    void save(false)
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{isEdit ? "编辑站点" : "新增站点"}</DialogTitle>
            <DialogDescription>
              站点拥有唯一的平台类型和入口地址；账号只保存身份、凭据与账号级设置。
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="site-name">站点名称</Label>
              <Input
                id="site-name"
                value={form.name}
                onChange={(event) => setForm((current) => ({ ...current, name: event.target.value }))}
                disabled={submitting}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="site-type">平台类型</Label>
              <Select
                value={form.type}
                onValueChange={(value) => setForm((current) => ({ ...current, type: value as UpstreamSiteType }))}
                disabled={submitting}
              >
                <SelectTrigger id="site-type" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="sub2api">Sub2API</SelectItem>
                  <SelectItem value="newapi">NewAPI</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="site-base-url">站点入口地址</Label>
              <Input
                id="site-base-url"
                type="url"
                placeholder="https://example.com"
                value={form.base_url}
                onChange={(event) => setForm((current) => ({ ...current, base_url: event.target.value }))}
                disabled={submitting}
                required
              />
              {baseURLChanged ? (
                <p className="text-xs text-warning">保存时需要确认；确认后将清除全部账号会话并暂停监控。</p>
              ) : null}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="site-order">排序</Label>
              <Input
                id="site-order"
                type="number"
                step="1"
                value={form.sort_order}
                onChange={(event) => setForm((current) => ({ ...current, sort_order: event.target.value }))}
                disabled={submitting}
              />
            </div>
            <label className="flex items-center justify-between border-y border-border py-3 text-sm">
              <span>忽略公告</span>
              <Switch
                checked={form.ignore_announcements}
                onCheckedChange={(value) => setForm((current) => ({ ...current, ignore_announcements: value }))}
                disabled={submitting}
              />
            </label>
            {error ? <p className="text-sm text-destructive">{error}</p> : null}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>取消</Button>
              <Button type="submit" disabled={submitting}>{submitting ? "保存中" : "保存"}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <AlertDialog open={confirmingBaseURL} onOpenChange={setConfirmingBaseURL}>
        <AlertDialogContent className="sm:max-w-md">
          <AlertDialogHeader>
            <div className="mb-1 flex size-9 items-center justify-center rounded-full bg-warning/10 text-warning">
              <AlertTriangle className="size-5" />
            </div>
            <AlertDialogTitle>确认修改站点入口地址</AlertDialogTitle>
            <AlertDialogDescription className="space-y-2">
              <span className="block">此变更会影响 {site?.account_count ?? 0} 个账号。</span>
              <span className="block">系统将清除这些账号的登录会话并暂停监控，不会自动向新地址发送任何凭据。</span>
              <span className="block">保存后，请逐个测试登录并手动恢复需要监控的账号。</span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={submitting}>返回修改</AlertDialogCancel>
            <AlertDialogAction onClick={(event) => { event.preventDefault(); void save(true) }} disabled={submitting}>
              {submitting ? "保存中" : "确认修改"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

"use client"

import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api"
import { useTriggerRefresh } from "@/lib/refresh-context"
import type { UpstreamAccount, UpstreamSite } from "@/lib/api-types"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export function AccountDeleteDialog({
  account,
  site,
  open,
  onOpenChange,
}: {
  account: UpstreamAccount | null
  site: UpstreamSite | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const refresh = useTriggerRefresh()
  const [replacementAccountID, setReplacementAccountID] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const replacementAccounts = useMemo(
    () => site?.accounts.filter((item) => item.id !== account?.id) ?? [],
    [account?.id, site?.accounts],
  )
  const needsReplacement = site?.default_account_id === account?.id && replacementAccounts.length > 0

  useEffect(() => {
    if (!open) return
    setReplacementAccountID("")
    setError(null)
  }, [open])

  async function submit() {
    if (!account || !site) return
    const replacementID = Number(replacementAccountID)
    if (needsReplacement && !replacementID) {
      setError("请选择新的默认账号")
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const query = needsReplacement ? `?replacement_account_id=${replacementID}` : ""
      await apiFetch(`/accounts/${account.id}${query}`, { method: "DELETE" })
      refresh()
      toast.success(`${account.alias} 已删除`)
      onOpenChange(false)
    } catch (reason) {
      setError((reason as Error).message || "删除账号失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>删除账号</DialogTitle>
          <DialogDescription>
            {account
              ? `${account.alias} 的历史、倍率快照与登录凭据将一并清除，且无法恢复。`
              : ""}
          </DialogDescription>
        </DialogHeader>
        {needsReplacement ? (
          <div className="space-y-1.5">
            <Label htmlFor="delete-replacement-account">新的默认账号</Label>
            <Select
              value={replacementAccountID}
              onValueChange={setReplacementAccountID}
              disabled={submitting}
            >
              <SelectTrigger id="delete-replacement-account" className="w-full">
                <SelectValue placeholder="选择替代账号" />
              </SelectTrigger>
              <SelectContent>
                {replacementAccounts.map((item) => (
                  <SelectItem key={item.id} value={String(item.id)}>
                    {item.alias}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ) : null}
        {error ? <p className="text-sm text-destructive">{error}</p> : null}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
            取消
          </Button>
          <Button variant="destructive" onClick={() => void submit()} disabled={submitting}>
            {submitting ? "删除中" : "删除"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

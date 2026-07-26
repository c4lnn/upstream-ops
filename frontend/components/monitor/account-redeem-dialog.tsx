"use client"

import { useEffect, useState, type FormEvent } from "react"
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
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api"
import { useTriggerRefresh } from "@/lib/refresh-context"
import type { AccountRedeemResult, UpstreamAccount } from "@/lib/api-types"

interface AccountRedeemDialogProps {
  open: boolean
  onOpenChange: (v: boolean) => void
  account: UpstreamAccount | null
  onSuccess?: (result: AccountRedeemResult) => void
}

export function AccountRedeemDialog({
  open,
  onOpenChange,
  account,
  onSuccess,
}: AccountRedeemDialogProps) {
  const [code, setCode] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const refresh = useTriggerRefresh()

  useEffect(() => {
    if (open) {
      setCode("")
      setError(null)
    }
  }, [open, account])

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (!account) return
    setSubmitting(true)
    setError(null)
    try {
      const result = await apiFetch<AccountRedeemResult>(`/accounts/${account.id}/redeem`, {
        method: "POST",
        body: JSON.stringify({ code }),
      })
      onSuccess?.(result)
      onOpenChange(false)
      refresh()
    } catch (e) {
      const err = e as Error
      setError(err.message || "兑换失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>兑换码</DialogTitle>
          <DialogDescription>
            {account ? account.alias : "输入兑换码后立即在线兑换。"}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="redeem-code">兑换码</Label>
            <Input
              id="redeem-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              placeholder="请输入兑换码"
              required
              disabled={submitting}
            />
            <p className="text-[11px] text-muted-foreground">区分大小写，提交后会直接调用上游在线兑换接口。</p>
          </div>

          {error ? (
            <p className="text-sm text-destructive" role="alert">
              {error}
            </p>
          ) : null}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
              取消
            </Button>
            <Button type="submit" disabled={submitting || !code.trim()}>
              {submitting ? "兑换中…" : "立即兑换"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

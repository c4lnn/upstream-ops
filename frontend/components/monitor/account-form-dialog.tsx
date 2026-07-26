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
import { Textarea } from "@/components/ui/textarea"
import { Switch } from "@/components/ui/switch"
import type {
  CredentialMode,
  RechargeMultiplierMode,
  UpstreamAccount,
  UpstreamSite,
} from "@/lib/api-types"
import { apiFetch } from "@/lib/api"
import { useTriggerRefresh } from "@/lib/refresh-context"
import { useCaptchaConfigs } from "@/lib/queries"
import { cn } from "@/lib/utils"

interface AccountFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  account?: UpstreamAccount | null
  site: UpstreamSite | null
}

interface FormState {
  alias: string
  username: string
  sort_order: string
  password: string
  login_extra_params: string
  credential_mode: CredentialMode
  newapi_token_kind: "cookie" | "access_token"
  newapi_cookie: string
  newapi_access_token: string
  newapi_user_id: string
  sub2api_access_token: string
  sub2api_refresh_token: string
  balance_threshold: string
  recharge_multiplier: string
  recharge_multiplier_mode: RechargeMultiplierMode
  monitor_enabled: boolean
  turnstile_enabled: boolean
  subscription_enabled: boolean
  proxy_enabled: boolean
  captcha_config_id: string
}

function initialState(account?: UpstreamAccount | null): FormState {
  return {
    alias: account?.alias ?? "",
    username: account?.username ?? "",
    sort_order: String(account?.sort_order ?? 1),
    password: "",
    login_extra_params: account?.login_extra_params ?? "",
    credential_mode: account?.credential_mode ?? "password",
    newapi_token_kind: "cookie",
    newapi_cookie: "",
    newapi_access_token: "",
    newapi_user_id: account?.user_id ?? "",
    sub2api_access_token: "",
    sub2api_refresh_token: "",
    balance_threshold: String(account?.balance_threshold ?? 0),
    recharge_multiplier:
      account?.recharge_multiplier != null ? String(account.recharge_multiplier) : "",
    recharge_multiplier_mode:
      account?.recharge_multiplier_mode === "multiply" ? "multiply" : "divide",
    monitor_enabled: account?.monitor_enabled ?? true,
    turnstile_enabled: account?.turnstile_enabled ?? false,
    subscription_enabled: account?.subscription_enabled ?? false,
    proxy_enabled: account?.proxy_enabled ?? false,
    captcha_config_id:
      account?.captcha_config_id != null ? String(account.captcha_config_id) : "",
  }
}

function buildTokenCredential(form: FormState, siteType: UpstreamSite["type"]) {
  if (siteType === "newapi") {
    if (form.newapi_token_kind === "access_token") {
      return JSON.stringify({
        access_token: form.newapi_access_token.trim(),
        user_id: form.newapi_user_id.trim(),
      })
    }
    return JSON.stringify({
      cookie: form.newapi_cookie.trim(),
      user_id: form.newapi_user_id.trim(),
    })
  }

  const refreshToken = form.sub2api_refresh_token.trim()
  return JSON.stringify({
    access_token: form.sub2api_access_token.trim(),
    ...(refreshToken ? { refresh_token: refreshToken } : {}),
  })
}

export function AccountFormDialog({
  open,
  onOpenChange,
  account,
  site,
}: AccountFormDialogProps) {
  const [form, setForm] = useState<FormState>(() => initialState(account))
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const refresh = useTriggerRefresh()
  const captchas = useCaptchaConfigs(open)

  useEffect(() => {
    if (!open) return
    setForm(initialState(account))
    setError(null)
  }, [account, open])

  const isEdit = Boolean(account)
  const isTokenMode = form.credential_mode === "token"
  const siteType = site?.type ?? "sub2api"
  const isNewAPI = siteType === "newapi"
  const supportsSubscription = siteType === "sub2api"
  const modeChanged = isEdit && form.credential_mode !== account?.credential_mode
  const existingNewAPIUserID = account?.user_id?.trim() ?? ""

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!site) {
      setError("请先选择所属站点")
      return
    }

    setSubmitting(true)
    setError(null)
    try {
      const alias = form.alias.trim()
      if (!alias) throw new Error("账号别名不能为空")

      const sortOrder = Number(form.sort_order)
      if (!Number.isInteger(sortOrder)) throw new Error("排序必须是整数")

      const balanceThreshold = Number(form.balance_threshold)
      if (!Number.isFinite(balanceThreshold) || balanceThreshold < 0) {
        throw new Error("余额阈值必须是非负数")
      }

      let rechargeMultiplier = 0
      if (form.recharge_multiplier.trim()) {
        rechargeMultiplier = Number(form.recharge_multiplier)
        if (!Number.isFinite(rechargeMultiplier) || rechargeMultiplier <= 0) {
          throw new Error("充值倍率必须大于 0，或留空跟随上游")
        }
      }

      const loginExtraParams = isTokenMode ? "" : form.login_extra_params.trim()
      if (loginExtraParams) {
        let parsed: unknown
        try {
          parsed = JSON.parse(loginExtraParams)
        } catch {
          throw new Error("附加表单参数 JSON 格式错误")
        }
        if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
          throw new Error("附加表单参数必须是 JSON 对象")
        }
      }

      if (!isTokenMode) {
        if (!isEdit && !form.username.trim()) throw new Error("请填写账号或邮箱")
        if (!isEdit && !form.password) throw new Error("新建时必须填写密码")
        if (modeChanged && !form.password) throw new Error("切换到账号密码模式时必须填写密码")
      }

      let tokenCredential = ""
      if (isTokenMode) {
        if (isNewAPI) {
          const credential =
            form.newapi_token_kind === "access_token"
              ? form.newapi_access_token.trim()
              : form.newapi_cookie.trim()
          const userID = form.newapi_user_id.trim()
          const userIDChanged = isEdit ? userID !== existingNewAPIUserID : Boolean(userID)
          if (!isEdit || modeChanged || credential || userIDChanged) {
            if (!credential) {
              throw new Error(
                form.newapi_token_kind === "access_token"
                  ? "NewAPI token 模式必须填写系统访问令牌"
                  : "NewAPI token 模式必须填写 Cookie",
              )
            }
            if (!userID) throw new Error("NewAPI token 模式必须填写 User ID")
          }
        } else if (
          !isEdit ||
          modeChanged ||
          form.sub2api_access_token ||
          form.sub2api_refresh_token
        ) {
          if (!form.sub2api_access_token.trim()) {
            throw new Error("Sub2API token 模式必须填写 Access Token")
          }
        }

        const credentialChanged = isNewAPI
          ? Boolean(
              form.newapi_token_kind === "access_token"
                ? form.newapi_access_token
                : form.newapi_cookie,
            ) || form.newapi_user_id.trim() !== existingNewAPIUserID
          : Boolean(form.sub2api_access_token || form.sub2api_refresh_token)
        if (!isEdit || modeChanged || credentialChanged) {
          tokenCredential = buildTokenCredential(form, site.type)
        }
      }

      const captchaConfigID =
        !isTokenMode && form.turnstile_enabled && form.captcha_config_id
          ? Number(form.captcha_config_id)
          : null
      if (!isTokenMode && form.turnstile_enabled && captchaConfigID == null) {
        throw new Error("启用 Turnstile 时必须选择一个打码 provider")
      }

      const body: Record<string, unknown> = {
        alias,
        username: form.username.trim(),
        sort_order: sortOrder,
        credential_mode: form.credential_mode,
        login_extra_params: loginExtraParams,
        balance_threshold: balanceThreshold,
        recharge_multiplier: rechargeMultiplier,
        recharge_multiplier_mode: form.recharge_multiplier_mode,
        monitor_enabled: form.monitor_enabled,
        turnstile_enabled: !isTokenMode && form.turnstile_enabled,
        subscription_enabled: supportsSubscription && form.subscription_enabled,
        proxy_enabled: form.proxy_enabled,
        captcha_config_id: captchaConfigID,
      }
      if (!isTokenMode && form.password) body.password = form.password
      if (isTokenMode && tokenCredential) body.token_credential = tokenCredential

      if (isEdit) {
        await apiFetch(`/accounts/${account!.id}`, {
          method: "PUT",
          body: JSON.stringify(body),
        })
      } else {
        await apiFetch(`/sites/${site.id}/accounts`, {
          method: "POST",
          body: JSON.stringify(body),
        })
      }

      onOpenChange(false)
      refresh()
    } catch (reason) {
      setError((reason as Error).message || "保存失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[90vh] max-w-md flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>{isEdit ? "编辑账号" : `新增账号${site ? ` · ${site.name}` : ""}`}</DialogTitle>
          <DialogDescription>
            {isEdit
              ? "账号凭据和运行设置独立保存；所属站点、类型和入口地址不可在此修改。"
              : "账号将继承此站点的类型和入口地址，凭据与运行状态独立保存。"}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="min-h-0 space-y-4 overflow-y-auto pr-1">
          {site ? (
            <div className="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs">
              <p className="font-medium text-foreground">{site.name} · {site.type === "newapi" ? "NewAPI" : "Sub2API"}</p>
              <p className="mt-1 truncate text-muted-foreground">{site.base_url}</p>
            </div>
          ) : null}

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_112px]">
            <div className="space-y-1.5">
              <Label htmlFor="account-alias">账号别名</Label>
              <Input
                id="account-alias"
                value={form.alias}
                onChange={(event) => setForm((current) => ({ ...current, alias: event.target.value }))}
                required
                disabled={submitting}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="account-sort-order">排序</Label>
              <Input
                id="account-sort-order"
                type="number"
                step="1"
                value={form.sort_order}
                onChange={(event) => setForm((current) => ({ ...current, sort_order: event.target.value }))}
                disabled={submitting}
              />
            </div>
          </div>

          <section className="space-y-2 border-t border-border pt-4">
            <Label>凭据类型</Label>
            <div className="grid grid-cols-2 gap-2 rounded-md border border-border p-1">
              <button
                type="button"
                disabled={submitting}
                onClick={() => setForm((current) => ({ ...current, credential_mode: "password" }))}
                className={cn(
                  "rounded px-3 py-1.5 text-xs font-medium transition-colors",
                  !isTokenMode ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted",
                )}
              >
                账号密码
              </button>
              <button
                type="button"
                disabled={submitting}
                onClick={() => setForm((current) => ({ ...current, credential_mode: "token" }))}
                className={cn(
                  "rounded px-3 py-1.5 text-xs font-medium transition-colors",
                  isTokenMode ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted",
                )}
              >
                Token
              </button>
            </div>
          </section>

          {!isTokenMode ? (
            <>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <Label htmlFor="account-username">账号 / 邮箱</Label>
                  <Input
                    id="account-username"
                    value={form.username}
                    onChange={(event) => setForm((current) => ({ ...current, username: event.target.value }))}
                    required={!isEdit || modeChanged}
                    disabled={submitting}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="account-password">{isEdit ? "新密码（留空不变）" : "密码"}</Label>
                  <Input
                    id="account-password"
                    type="password"
                    value={form.password}
                    onChange={(event) => setForm((current) => ({ ...current, password: event.target.value }))}
                    required={!isEdit || modeChanged}
                    disabled={submitting}
                  />
                </div>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="account-login-extra-params">附加登录参数（可选）</Label>
                <Textarea
                  id="account-login-extra-params"
                  value={form.login_extra_params}
                  onChange={(event) => setForm((current) => ({ ...current, login_extra_params: event.target.value }))}
                  placeholder='例如 {"invite_code":"..."}'
                  rows={2}
                  disabled={submitting}
                />
              </div>
            </>
          ) : (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="account-token-note">备注（可选）</Label>
                <Input
                  id="account-token-note"
                  value={form.username}
                  onChange={(event) => setForm((current) => ({ ...current, username: event.target.value }))}
                  placeholder="用于识别该账号"
                  disabled={submitting}
                />
              </div>
              {isNewAPI ? (
                <NewAPITokenFields form={form} setForm={setForm} disabled={submitting} isEdit={isEdit} />
              ) : (
                <div className="space-y-3">
                  <div className="space-y-1.5">
                    <Label htmlFor="sub2api-access-token">Access Token</Label>
                    <Textarea
                      id="sub2api-access-token"
                      value={form.sub2api_access_token}
                      onChange={(event) => setForm((current) => ({ ...current, sub2api_access_token: event.target.value }))}
                      placeholder={isEdit ? "留空不修改" : "粘贴 Access Token"}
                      rows={3}
                      className="font-mono text-xs"
                      disabled={submitting}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="sub2api-refresh-token">Refresh Token（可选）</Label>
                    <Textarea
                      id="sub2api-refresh-token"
                      value={form.sub2api_refresh_token}
                      onChange={(event) => setForm((current) => ({ ...current, sub2api_refresh_token: event.target.value }))}
                      placeholder="留空不修改"
                      rows={2}
                      className="font-mono text-xs"
                      disabled={submitting}
                    />
                  </div>
                </div>
              )}
            </>
          )}

          <section className="space-y-3 border-t border-border pt-4">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="account-balance-threshold">余额阈值</Label>
                <Input
                  id="account-balance-threshold"
                  type="number"
                  min="0"
                  step="any"
                  value={form.balance_threshold}
                  onChange={(event) => setForm((current) => ({ ...current, balance_threshold: event.target.value }))}
                  disabled={submitting}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="account-recharge-multiplier">充值倍率（可选）</Label>
                <Input
                  id="account-recharge-multiplier"
                  type="number"
                  min="0"
                  step="any"
                  value={form.recharge_multiplier}
                  onChange={(event) => setForm((current) => ({ ...current, recharge_multiplier: event.target.value }))}
                  disabled={submitting}
                />
              </div>
            </div>
            <label className="flex items-center justify-between border-y border-border py-2.5 text-sm">
              <span>启用监控</span>
              <Switch
                checked={form.monitor_enabled}
                onCheckedChange={(value) => setForm((current) => ({ ...current, monitor_enabled: value }))}
                disabled={submitting}
              />
            </label>
            <label className="flex items-center justify-between text-sm">
              <span>通过代理访问</span>
              <Switch
                checked={form.proxy_enabled}
                onCheckedChange={(value) => setForm((current) => ({ ...current, proxy_enabled: value }))}
                disabled={submitting}
              />
            </label>
            {supportsSubscription ? (
              <label className="flex items-center justify-between text-sm">
                <span>订阅监控</span>
                <Switch
                  checked={form.subscription_enabled}
                  onCheckedChange={(value) => setForm((current) => ({ ...current, subscription_enabled: value }))}
                  disabled={submitting}
                />
              </label>
            ) : null}
            {!isTokenMode ? (
              <>
                <label className="flex items-center justify-between text-sm">
                  <span>登录时处理 Turnstile</span>
                  <Switch
                    checked={form.turnstile_enabled}
                    onCheckedChange={(value) => setForm((current) => ({ ...current, turnstile_enabled: value }))}
                    disabled={submitting}
                  />
                </label>
                {form.turnstile_enabled ? (
                  <div className="space-y-1.5">
                    <Label htmlFor="account-captcha-config">打码 provider</Label>
                    <select
                      id="account-captcha-config"
                      value={form.captcha_config_id}
                      onChange={(event) => setForm((current) => ({ ...current, captcha_config_id: event.target.value }))}
                      className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
                      disabled={submitting || captchas.loading}
                    >
                      <option value="">选择 provider</option>
                      {(captchas.data ?? []).filter((item) => item.enabled).map((item) => (
                        <option key={item.id} value={item.id}>{item.name}</option>
                      ))}
                    </select>
                  </div>
                ) : null}
              </>
            ) : null}
          </section>

          {error ? <p className="text-sm text-destructive">{error}</p> : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>取消</Button>
            <Button type="submit" disabled={submitting || !site}>{submitting ? "保存中" : "保存"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function NewAPITokenFields({
  form,
  setForm,
  disabled,
  isEdit,
}: {
  form: FormState
  setForm: React.Dispatch<React.SetStateAction<FormState>>
  disabled: boolean
  isEdit: boolean
}) {
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-2 rounded-md border border-border p-1">
        <button
          type="button"
          disabled={disabled}
          onClick={() => setForm((current) => ({ ...current, newapi_token_kind: "cookie" }))}
          className={cn(
            "rounded px-3 py-1.5 text-xs font-medium transition-colors",
            form.newapi_token_kind === "cookie" ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted",
          )}
        >
          Cookie
        </button>
        <button
          type="button"
          disabled={disabled}
          onClick={() => setForm((current) => ({ ...current, newapi_token_kind: "access_token" }))}
          className={cn(
            "rounded px-3 py-1.5 text-xs font-medium transition-colors",
            form.newapi_token_kind === "access_token" ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted",
          )}
        >
          系统访问令牌
        </button>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="newapi-token">
          {form.newapi_token_kind === "cookie" ? "Cookie" : "系统访问令牌"}
        </Label>
        <Textarea
          id="newapi-token"
          value={form.newapi_token_kind === "cookie" ? form.newapi_cookie : form.newapi_access_token}
          onChange={(event) =>
            setForm((current) =>
              current.newapi_token_kind === "cookie"
                ? { ...current, newapi_cookie: event.target.value }
                : { ...current, newapi_access_token: event.target.value },
            )
          }
          placeholder={isEdit ? "留空不修改" : "粘贴凭据"}
          rows={3}
          className="font-mono text-xs"
          disabled={disabled}
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="newapi-user-id">User ID</Label>
        <Input
          id="newapi-user-id"
          value={form.newapi_user_id}
          onChange={(event) => setForm((current) => ({ ...current, newapi_user_id: event.target.value }))}
          disabled={disabled}
        />
      </div>
    </div>
  )
}

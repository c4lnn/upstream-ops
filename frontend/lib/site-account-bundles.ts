import { apiFetch, getToken, notifyUnauthorized } from "@/lib/api"
import type {
  SiteAccountBundleImportPlan,
  SiteAccountBundleImportResult,
  SiteAccountBundleImportStrategy,
} from "@/lib/api-types"

export interface SiteAccountBundleExportOptions {
  siteIDs: number[]
  includeCredentials: boolean
  password?: string
}

export async function downloadSiteAccountBundle(options: SiteAccountBundleExportOptions) {
  const headers = new Headers({
    Accept: "application/json",
    "Content-Type": "application/json",
  })
  const token = getToken()
  if (token) headers.set("Authorization", `Bearer ${token}`)
  const response = await fetch("/api/site-account-bundles/export", {
    method: "POST",
    headers,
    body: JSON.stringify({
      site_ids: options.siteIDs,
      include_credentials: options.includeCredentials,
      password: options.password ?? "",
    }),
  })
  if (response.status === 401) notifyUnauthorized()
  if (!response.ok) {
    throw new Error(await responseError(response))
  }
  const blob = await response.blob()
  const disposition = response.headers.get("Content-Disposition") ?? ""
  const matched = disposition.match(/filename="?([^";]+)"?/i)
  const filename = matched?.[1] ?? "upstream-ops-site-account-bundle.json"
  const url = URL.createObjectURL(blob)
  try {
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = filename
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

export function previewSiteAccountBundle(
  file: File,
  strategy: SiteAccountBundleImportStrategy,
  password: string,
  confirmBaseURLChanges: boolean,
) {
  return apiFetch<SiteAccountBundleImportPlan>("/site-account-bundles/import/preview", {
    method: "POST",
    body: importForm(file, strategy, password, confirmBaseURLChanges),
  })
}

export function commitSiteAccountBundle(
  file: File,
  strategy: SiteAccountBundleImportStrategy,
  password: string,
  digest: string,
  confirmBaseURLChanges: boolean,
) {
  const form = importForm(file, strategy, password, confirmBaseURLChanges)
  form.set("digest", digest)
  return apiFetch<SiteAccountBundleImportResult>("/site-account-bundles/import", {
    method: "POST",
    body: form,
  })
}

function importForm(
  file: File,
  strategy: SiteAccountBundleImportStrategy,
  password: string,
  confirmBaseURLChanges: boolean,
) {
  const form = new FormData()
  form.set("file", file)
  form.set("strategy", strategy)
  if (password) form.set("password", password)
  form.set("confirm_base_url_changes", String(confirmBaseURLChanges))
  return form
}

async function responseError(response: Response) {
  const text = await response.text()
  if (!text) return `HTTP ${response.status}`
  try {
    const parsed = JSON.parse(text) as { error?: string }
    return parsed.error ?? text
  } catch {
    return text
  }
}

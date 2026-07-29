export type RefreshScope = "monitor-snapshots"

export interface RefreshVersions {
  global: number
  scopes: Record<RefreshScope, number>
}

export interface RefreshOptions {
  notify?: boolean
  scope?: RefreshScope
}

export const initialRefreshVersions: RefreshVersions = {
  global: 0,
  scopes: { "monitor-snapshots": 0 },
}

export function advanceRefreshVersion(
  current: RefreshVersions,
  scope?: RefreshScope,
): RefreshVersions {
  if (!scope) return { ...current, global: current.global + 1 }
  return {
    ...current,
    scopes: { ...current.scopes, [scope]: current.scopes[scope] + 1 },
  }
}

export function refreshVersionKey(versions: RefreshVersions, scope?: RefreshScope) {
  return scope ? `${versions.global}:${versions.scopes[scope]}` : String(versions.global)
}

export function isTrackedRefresh(options?: RefreshOptions) {
  return options?.scope == null
}

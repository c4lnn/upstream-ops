"use client"

import {
  createContext,
  useContext,
  useState,
  useCallback,
  useEffect,
  useRef,
  type ReactNode,
} from "react"
import { RefreshRequestTracker } from "@/lib/refresh-tracker"

interface RefreshContextValue {
  tick: number
  bump: (options?: { notify?: boolean }) => void
  refreshing: boolean
  result: { cycle: number; status: "success" | "failed" } | null
  trackRequest: (tick: number, request: Promise<unknown>) => void
}

const RefreshContext = createContext<RefreshContextValue>({
  tick: 0,
  bump: () => {},
  refreshing: false,
  result: null,
  trackRequest: () => {},
})

/** 全局后台轮询周期；后端 cron 是分钟级，这里 30s 已足够"显得活着"。 */
const POLL_INTERVAL_MS = 30_000

export function RefreshProvider({ children }: { children: ReactNode }) {
  const [tick, setTick] = useState(0)
  const [refreshing, setRefreshing] = useState(false)
  const [result, setResult] = useState<RefreshContextValue["result"]>(null)
  const tickRef = useRef(0)
  const notifyRef = useRef(false)
  const refreshingRef = useRef(false)
  const trackerRef = useRef<RefreshRequestTracker | null>(null)
  if (trackerRef.current == null) {
    trackerRef.current = new RefreshRequestTracker((status) => {
      refreshingRef.current = false
      setRefreshing(false)
      if (notifyRef.current) setResult({ cycle: tickRef.current, status })
      notifyRef.current = false
    })
  }

  const bump = useCallback((options?: { notify?: boolean }) => {
    const next = ++tickRef.current
    notifyRef.current = options?.notify === true
    trackerRef.current?.start(next)
    refreshingRef.current = true
    setRefreshing(true)
    setTick(next)

    // 页面没有活动查询时也必须结束本轮刷新。
    setTimeout(() => {
      trackerRef.current?.finishIfIdle(next)
    }, 0)
  }, [])

  const trackRequest = useCallback((requestTick: number, request: Promise<unknown>) => {
    trackerRef.current?.track(requestTick, request)
  }, [])

  useEffect(() => () => trackerRef.current?.cancel(), [])

  // 30 秒静默 polling。页面在后台标签时（document.hidden）不轮询，避免后台浪费请求。
  useEffect(() => {
    const id = setInterval(() => {
      if (typeof document !== "undefined" && document.hidden) return
      if (refreshingRef.current) return
      const next = ++tickRef.current
      setTick(next)
    }, POLL_INTERVAL_MS)
    return () => clearInterval(id)
  }, [])

  return (
    <RefreshContext.Provider value={{ tick, bump, refreshing, result, trackRequest }}>
      {children}
    </RefreshContext.Provider>
  )
}

/** useRefreshTick 在 tick 变化时让组件重新拉数据。 */
export function useRefreshTick() {
  return useContext(RefreshContext).tick
}

/** useTriggerRefresh 返回手动 bump 的方法，比如点头部的"刷新"按钮。 */
export function useTriggerRefresh() {
  return useContext(RefreshContext).bump
}

export function useRefreshStatus() {
  const context = useContext(RefreshContext)
  return { refreshing: context.refreshing, result: context.result }
}

export function useTrackRefreshRequest() {
  return useContext(RefreshContext).trackRequest
}

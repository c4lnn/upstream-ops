"use client"

import { createContext, useCallback, useContext, useState, type ReactNode } from "react"
import { SiteFormDialog } from "@/components/monitor/site-form-dialog"

interface AddSiteContextValue {
  openAddSite: () => void
}

const AddSiteContext = createContext<AddSiteContextValue | null>(null)

export function AddSiteProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const openAddSite = useCallback(() => setOpen(true), [])

  return (
    <AddSiteContext.Provider value={{ openAddSite }}>
      {children}
      <SiteFormDialog site={null} open={open} onOpenChange={setOpen} />
    </AddSiteContext.Provider>
  )
}

export function useAddSite(): AddSiteContextValue {
  const context = useContext(AddSiteContext)
  if (!context) throw new Error("useAddSite must be used inside AddSiteProvider")
  return context
}

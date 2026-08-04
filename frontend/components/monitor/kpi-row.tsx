"use client"

import { ArrowUpRight, DollarSign, MessageSquare, TrendingUp } from "lucide-react"
import { Card } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { useDashboardSummary, useRateChanges } from "@/lib/queries"
import { money } from "@/lib/format"
import type { LucideIcon } from "lucide-react"
import type { ReactNode } from "react"

interface Kpi {
  label: string
  value: ReactNode
  icon: LucideIcon
  iconBg: string
  iconColor: string
  footer: ReactNode
}

function countTodayChanges(changes: { changed_at: string }[]) {
  const startOfDay = new Date()
  startOfDay.setHours(0, 0, 0, 0)
  return changes.filter((c) => new Date(c.changed_at) >= startOfDay).length
}

export function KpiRow() {
  const summary = useDashboardSummary()
  const recentChanges = useRateChanges(100)

  const data = summary.data
  const total = data?.total_channels ?? 0
  const active = data?.active_channels ?? 0
  const failed = data?.failed_channels ?? 0
  const totalBalance = data?.total_balance ?? 0
  const totalUsage = data?.total_usage ?? 0
  const lowest = data?.lowest_balance ?? null

  const todayChangeCount = countTodayChanges(recentChanges.data ?? [])

  const kpis: Kpi[] = [
    {
      label: "总余额",
      value: money(totalBalance),
      icon: DollarSign,
      iconBg: "bg-brand/10",
      iconColor: "text-brand",
      footer: lowest ? (
        <span className="text-muted-foreground">
          {"最低："}
          <span className="font-medium text-foreground">{lowest.name}</span>
          {" "}
          <span className="text-warning">{money(lowest.balance)}</span>
        </span>
      ) : (
        <span className="text-muted-foreground">{"—"}</span>
      ),
    },
    {
      label: "总用量",
      value: money(totalUsage),
      icon: TrendingUp,
      iconBg: "bg-warning/10",
      iconColor: "text-warning",
      footer: <span className="text-muted-foreground">{"所有渠道累计"}</span>,
    },
    {
      label: "渠道状态",
      value: (
        <span>
          {active}
          <span className="mx-1 text-lg font-normal text-muted-foreground">{"/"}</span>
          <span className="text-lg font-normal text-muted-foreground">{total}</span>
        </span>
      ),
      icon: MessageSquare,
      iconBg: "bg-success/10",
      iconColor: "text-success",
      footer: (
        <span className="text-muted-foreground">
          <span className="text-success font-medium">{active} 健康</span>
          {failed > 0 ? (
            <>
              {" · "}
              <span className="text-danger font-medium">{failed} 失败</span>
            </>
          ) : null}
        </span>
      ),
    },
    {
      label: "今日倍率变动",
      value: (
        <span className={cn(todayChangeCount > 0 ? "text-danger" : "text-foreground")}>
          {todayChangeCount}
        </span>
      ),
      icon: ArrowUpRight,
      iconBg: "bg-danger/10",
      iconColor: "text-danger",
      footer: (
        <span className="text-muted-foreground">
          {todayChangeCount > 0 ? `检测到 ${todayChangeCount} 次变动` : "今日无变动"}
        </span>
      ),
    },
  ]

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {kpis.map((k) => (
        <Card
          key={k.label}
          className="flex flex-row items-start justify-between gap-0 border border-border p-4 shadow-none"
        >
          <div className="flex min-w-0 flex-col">
            <span className="text-xs text-muted-foreground">{k.label}</span>
            <p className="mt-1 text-2xl font-bold tracking-tight text-foreground">{k.value}</p>
            <p className="mt-1 text-xs">{k.footer}</p>
          </div>
          <span className={cn("flex size-10 shrink-0 items-center justify-center rounded-xl", k.iconBg)}>
            <k.icon className={cn("size-5", k.iconColor)} />
          </span>
        </Card>
      ))}
    </div>
  )
}

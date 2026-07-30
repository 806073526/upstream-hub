"use client"

import { useMemo, useState } from "react"
import { Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis, CartesianGrid } from "recharts"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useBalanceTrend, useDashboardSummary, type BalanceTrendRange } from "@/lib/queries"
import { money } from "@/lib/format"
import { cn } from "@/lib/utils"

function formatY(n: number) {
  if (n === 0) return "$0"
  if (n >= 1000) return `$${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}K`
  if (n >= 100) return `$${n.toFixed(0)}`
  return `$${n.toFixed(n >= 10 ? 1 : 2)}`
}

/**
 * niceCeil 把最大值向上取整到一个"好看的"刻度，避免曲线贴顶。
 * 例如 47 → 50；478 → 500；12,300 → 15,000。
 */
function niceCeil(n: number): number {
  if (!Number.isFinite(n) || n <= 0) return 10
  const padded = n * 1.15
  const mag = Math.pow(10, Math.floor(Math.log10(padded)))
  const norm = padded / mag
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10
  return step * mag
}

function formatDay(iso: string) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.getMonth() + 1}月${d.getDate()}日`
}

function formatHour(iso: string) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const now = new Date()
  const time = d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false })
  if (
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  ) {
    return time
  }
  return `${d.getMonth() + 1}/${d.getDate()} ${time}`
}

interface TooltipPayloadItem { value: number }

function ChartTooltip({ active, payload, label }: { active?: boolean; payload?: TooltipPayloadItem[]; label?: string }) {
  if (!active || !payload?.length) return null
  return (
    <div className="rounded-lg border border-border bg-popover px-3 py-2 shadow-md">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="text-sm font-semibold text-foreground">
        {"$"}{payload[0].value.toLocaleString("en-US")}
      </p>
    </div>
  )
}

export function BalanceOverview() {
  const [range, setRange] = useState<BalanceTrendRange>("7d")
  const [disabledChannelIDs, setDisabledChannelIDs] = useState<Set<number>>(() => new Set())
  const summary = useDashboardSummary()
  const channels = summary.data?.channels ?? []
  const selectedChannelIDs = useMemo(() => {
    if (disabledChannelIDs.size === 0) return undefined
    return channels.filter((channel) => !disabledChannelIDs.has(channel.id)).map((channel) => channel.id)
  }, [channels, disabledChannelIDs])
  const trend = useBalanceTrend(range, selectedChannelIDs)

  const data = (trend.data ?? []).map((p) => ({
    day: range === "24h" ? formatHour(p.day) : formatDay(p.day),
    balance: p.balance,
  }))

  const currentTotal = summary.data == null
    ? null
    : channels.reduce((total, channel) => (
      disabledChannelIDs.has(channel.id) ? total : total + (channel.last_balance ?? 0)
    ), 0)
  const allDisabled = channels.length > 0 && selectedChannelIDs?.length === 0
  const yMax = data.length > 0 ? niceCeil(Math.max(...data.map((d) => d.balance))) : 10

  function toggleChannel(channelID: number) {
    setDisabledChannelIDs((current) => {
      const next = new Set(current)
      if (next.has(channelID)) next.delete(channelID)
      else next.add(channelID)
      return next
    })
  }

  return (
    <Card data-testid="balance-overview" className="h-100 border border-border shadow-none">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between pb-2">
        <div className="min-w-0">
          <CardTitle className="text-base font-semibold">{"余额概览"}</CardTitle>
          <p className="mt-1 text-xs text-muted-foreground">
            {"当前合计 "}
            <strong className="font-semibold text-foreground">{money(currentTotal)}</strong>
          </p>
        </div>
        <div className="inline-flex shrink-0 rounded-md border border-border bg-muted/30 p-0.5">
          {([
            ["7d", "7 天"],
            ["24h", "24 小时"],
          ] as const).map(([value, label]) => (
            <button
              key={value}
              type="button"
              onClick={() => setRange(value)}
              className={cn(
                "h-6 rounded px-2 text-xs transition-colors",
                range === value
                  ? "bg-background text-foreground shadow-xs"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <div className="min-h-0 w-full flex-1">
          {allDisabled ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">
              {"全部渠道已移除统计，可从下方图例重新启用"}
            </div>
          ) : trend.loading ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">{"加载中…"}</div>
          ) : data.length === 0 ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
              {range === "24h" ? "暂无 24 小时余额采样，等待下次扫描或手动刷新" : "暂无余额采样，等待下次扫描或手动刷新"}
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={data} margin={{ top: 8, right: 12, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
                <XAxis
                  dataKey="day"
                  tickLine={false}
                  axisLine={false}
                  tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
                  dy={8}
                />
                <YAxis
                  tickLine={false}
                  axisLine={false}
                  width={48}
                  tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
                  tickFormatter={formatY}
                  domain={[0, yMax]}
                />
                <Tooltip content={<ChartTooltip />} cursor={{ stroke: "var(--border)", strokeDasharray: "4 4" }} />
                <Line
                  type="monotone"
                  dataKey="balance"
                  stroke="var(--brand)"
                  strokeWidth={2}
                  dot={{ r: 4, fill: "var(--background)", stroke: "var(--brand)", strokeWidth: 2 }}
                  activeDot={{ r: 5, fill: "var(--brand)", strokeWidth: 0 }}
                />
              </LineChart>
            </ResponsiveContainer>
          )}
        </div>

        {/* per-channel chips */}
        {channels.length > 0 ? (
          <div className="mt-3 flex shrink-0 flex-wrap items-center gap-x-5 gap-y-2 border-t border-border pt-3">
            {channels.map((c) => {
              const isFailed = !!c.last_error
              const isUnknown = c.last_balance == null
              const selected = !disabledChannelIDs.has(c.id)
              return (
                <button
                  key={c.id}
                  type="button"
                  aria-pressed={selected}
                  aria-label={`${c.name}，${selected ? "已纳入统计" : "已移除统计"}`}
                  onClick={() => toggleChannel(c.id)}
                  className={cn(
                    "inline-flex min-h-7 items-center gap-1.5 text-xs transition-colors",
                    selected ? "text-foreground" : "text-muted-foreground/50 hover:text-muted-foreground",
                  )}
                >
                  <span
                    className={cn(
                      "size-2 rounded-full",
                      !selected
                        ? "bg-muted-foreground/40"
                        : isFailed ? "bg-danger" : isUnknown ? "bg-muted-foreground/40" : "bg-success",
                    )}
                  />
                  <span className={cn("font-medium", selected ? "text-foreground" : "text-current")}>{c.name}</span>
                  <span className={cn("tabular-nums", selected ? "text-muted-foreground" : "text-current")}>
                    {money(c.last_balance)}
                  </span>
                </button>
              )
            })}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}

"use client"

import { useMemo, useState } from "react"
import { AlertCircle } from "lucide-react"
import { Bar, BarChart, CartesianGrid, LabelList, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { money } from "@/lib/format"
import { useUsageTrend } from "@/lib/queries"
import { cn } from "@/lib/utils"
import {
  buildFilteredUsageChart,
  channelDataKey,
  formatUsageInterval,
  formatUsageTick,
  type UsageChartRow,
} from "@/lib/usage-chart-data"
import type { UsageTrendChannel, UsageTrendRange } from "@/lib/api-types"

const ranges: Array<{ value: UsageTrendRange; label: string }> = [
  { value: "1h", label: "1 小时" },
  { value: "today", label: "今日" },
  { value: "24h", label: "24 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
]

const seriesColors = [
  "var(--chart-1)",
  "var(--chart-2)",
  "var(--chart-4)",
  "var(--brand)",
  "var(--success)",
  "var(--danger)",
  "var(--chart-3)",
  "var(--warning)",
]

const qualityLabels: Record<string, string> = {
  exact: "精确",
  observed: "采样推算",
  mixed: "混合精度",
  missing: "无数据",
}

function resolutionLabel(seconds: number) {
  if (seconds >= 86400) return `${seconds / 86400} 天`
  if (seconds >= 3600) return `${seconds / 3600} 小时`
  return `${seconds / 60} 分钟`
}

function formatYAxis(value: number) {
  if (value === 0) return "$0"
  if (value >= 1000) return `$${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}K`
  if (value >= 10) return `$${value.toFixed(0)}`
  return `$${value.toFixed(value >= 1 ? 1 : 2)}`
}

function formatBarAmount(value: number) {
  if (value >= 1000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}K`
  if (value >= 100) return value.toFixed(0)
  if (value >= 10) return value.toFixed(1)
  if (value >= 1) return value.toFixed(2).replace(/\.00$/, "")
  return value.toFixed(value < 0.01 ? 3 : 2).replace(/0+$/, "").replace(/\.$/, "")
}

interface UsageBarLabelProps {
  x?: number | string
  y?: number | string
  width?: number | string
  index?: number
  channelID: number
  channels: UsageTrendChannel[]
  data: UsageChartRow[]
}

function UsageBarLabel({ x, y, width, index, channelID, channels, data }: UsageBarLabelProps) {
  const row = index == null ? undefined : data[index]
  const numericX = Number(x)
  const numericY = Number(y)
  const numericWidth = Number(width)
  if (!row || !Number.isFinite(numericX) || !Number.isFinite(numericY) || numericWidth < 14) return null

  const topChannel = [...channels].reverse().find((channel) => (
    typeof row[channelDataKey(channel.id)] === "number" && Number(row[channelDataKey(channel.id)]) > 0
  ))
  const total = row.selectedTotalAmount
  if (topChannel?.id !== channelID || typeof total !== "number" || total <= 0) return null

  return (
    <text
      data-testid="usage-bar-label"
      x={numericX + numericWidth / 2}
      y={numericY - 6}
      textAnchor="middle"
      className="fill-muted-foreground text-[10px] tabular-nums"
    >
      {formatBarAmount(total)}
    </text>
  )
}

interface UsageTooltipProps {
  active?: boolean
  payload?: Array<{ payload: UsageChartRow }>
  channels: UsageTrendChannel[]
  allChannels: UsageTrendChannel[]
  range: UsageTrendRange
}

function UsageTooltip({ active, payload, channels, allChannels, range }: UsageTooltipProps) {
  const row = payload?.[0]?.payload
  if (!active || !row) return null

  return (
    <div className="min-w-48 rounded-md border border-border bg-popover px-3 py-2 shadow-lg">
      <p className="text-xs font-medium text-foreground">
        {formatUsageInterval(row.startAt, row.endAt, range)}
      </p>
      <div className="mt-2 space-y-1">
        {channels.map((channel, index) => {
          const value = row[channelDataKey(channel.id)]
          const colorIndex = allChannels.findIndex((item) => item.id === channel.id)
          return (
            <div key={channel.id} className="flex items-center justify-between gap-5 text-xs">
              <span className="inline-flex min-w-0 items-center gap-1.5 text-muted-foreground">
                <span
                  className="size-2 shrink-0 rounded-sm"
                  style={{ backgroundColor: seriesColors[(colorIndex < 0 ? index : colorIndex) % seriesColors.length] }}
                />
                <span className="truncate">{channel.name}</span>
              </span>
              <span className="shrink-0 font-mono font-medium tabular-nums text-foreground">
                {typeof value === "number" ? money(value, { precise: value > 0 && value < 0.01 }) : "缺失"}
              </span>
            </div>
          )
        })}
      </div>
      <div className="mt-2 flex items-center justify-between border-t border-border pt-2 text-xs">
        <span className="text-muted-foreground">{qualityLabels[row.quality] ?? row.quality}</span>
        <span className="font-semibold tabular-nums text-foreground">{money(row.selectedTotalAmount)}</span>
      </div>
    </div>
  )
}

export function UsageOverview() {
  const [range, setRange] = useState<UsageTrendRange>("24h")
  const [disabledChannelIDs, setDisabledChannelIDs] = useState<Set<number>>(() => new Set())
  const trend = useUsageTrend(range)
  const filtered = useMemo(
    () => trend.data ? buildFilteredUsageChart(trend.data, disabledChannelIDs) : null,
    [disabledChannelIDs, trend.data],
  )
  const data = filtered?.rows ?? []
  const channels = trend.data?.channels ?? []
  const selectedChannels = filtered?.channels ?? []
  const hasData = selectedChannels.length > 0 && data.some((point) => point.selectedTotalAmount !== null)

  function toggleChannel(channelID: number) {
    setDisabledChannelIDs((current) => {
      const next = new Set(current)
      if (next.has(channelID)) next.delete(channelID)
      else next.add(channelID)
      return next
    })
  }

  return (
    <Card data-testid="usage-overview" className="gap-3 border border-border shadow-none">
      <CardHeader className="gap-3 pb-0 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <CardTitle className="text-base font-semibold">{"阶段用量"}</CardTitle>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span>
              {"区间合计 "}
              <strong className="font-semibold text-foreground">{money(filtered?.rangeTotalAmount)}</strong>
            </span>
            {trend.data ? <span>{`每 ${resolutionLabel(trend.data.output_resolution_seconds)}`}</span> : null}
            {trend.data && !trend.data.complete ? (
              <span className="inline-flex items-center gap-1 text-warning">
                <AlertCircle className="size-3" />
                {"部分时段缺失"}
              </span>
            ) : null}
          </div>
        </div>
        <div className="max-w-full overflow-x-auto pb-0.5">
          <ToggleGroup
            type="single"
            variant="outline"
            size="sm"
            value={range}
            onValueChange={(value) => value && setRange(value as UsageTrendRange)}
            className="min-w-max bg-muted/20"
            aria-label="用量统计范围"
          >
            {ranges.map((item) => (
              <ToggleGroupItem key={item.value} value={item.value} className="h-7 px-2.5 text-xs">
                {item.label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </div>
      </CardHeader>

      <CardContent>
        <div className="h-72 min-h-72 w-full sm:h-80 sm:min-h-80">
          {trend.loading && !trend.data ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">{"加载中…"}</div>
          ) : trend.error ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-danger">{trend.error}</div>
          ) : selectedChannels.length === 0 ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">
              {"全部渠道已移除统计，可从下方图例重新启用"}
            </div>
          ) : !hasData ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">
              {"暂无阶段用量，等待定时采集或手动同步"}
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={data} margin={{ top: 26, right: 8, left: 0, bottom: 2 }} barCategoryGap="18%">
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
                <XAxis
                  dataKey="startAt"
                  tickLine={false}
                  axisLine={false}
                  minTickGap={range === "1h" ? 22 : range === "30d" ? 18 : 40}
                  tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
                  tickFormatter={(value) => formatUsageTick(String(value), range)}
                  dy={8}
                />
                <YAxis
                  tickLine={false}
                  axisLine={false}
                  width={48}
                  tick={{ fill: "var(--muted-foreground)", fontSize: 11 }}
                  tickFormatter={formatYAxis}
                  domain={[0, "auto"]}
                />
                <Tooltip
                  content={<UsageTooltip channels={selectedChannels} allChannels={channels} range={range} />}
                  cursor={{ fill: "var(--muted)", opacity: 0.45 }}
                />
                {selectedChannels.map((channel) => {
                  const colorIndex = channels.findIndex((item) => item.id === channel.id)
                  return (
                    <Bar
                      key={channel.id}
                      dataKey={channelDataKey(channel.id)}
                      name={channel.name}
                      stackId="usage"
                      fill={seriesColors[colorIndex % seriesColors.length]}
                      maxBarSize={range === "1h" ? 34 : 28}
                      isAnimationActive={false}
                    >
                      <LabelList
                        content={(props) => (
                          <UsageBarLabel
                            {...props}
                            channelID={channel.id}
                            channels={selectedChannels}
                            data={data}
                          />
                        )}
                      />
                    </Bar>
                  )
                })}
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>

        {channels.length > 0 ? (
          <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 border-t border-border pt-3">
            {channels.map((channel, index) => {
              const selected = !disabledChannelIDs.has(channel.id)
              return (
                <button
                  key={channel.id}
                  type="button"
                  aria-pressed={selected}
                  aria-label={`${channel.name}，${selected ? "已纳入统计" : "已移除统计"}`}
                  onClick={() => toggleChannel(channel.id)}
                  className={cn(
                    "inline-flex min-h-7 items-center gap-1.5 text-xs transition-colors",
                    selected ? "text-foreground" : "text-muted-foreground/50 hover:text-muted-foreground",
                  )}
                >
                  <span
                    className="size-2 rounded-sm"
                    style={{
                      backgroundColor: selected
                        ? seriesColors[index % seriesColors.length]
                        : "var(--muted-foreground)",
                    }}
                  />
                  <span className={cn("font-medium", selected ? "text-foreground" : "text-current")}>
                    {channel.name}
                  </span>
                  <span className={cn("tabular-nums", selected ? "text-muted-foreground" : "text-current")}>
                    {money(trend.data?.channel_totals[String(channel.id)])}
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

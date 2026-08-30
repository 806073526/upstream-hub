"use client"

import { useMemo, useState } from "react"
import { AlertCircle, ChevronLeft, ChevronRight, CircleDollarSign, LineChart as LineChartIcon, LoaderCircle, ReceiptText } from "lucide-react"
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cny } from "@/lib/format"
import { useProfitDetails, useProfitTrend } from "@/lib/queries"
import type { ProfitCostDetail, ProfitDetailKind, ProfitReconciliation, ProfitSaleDetail, ProfitTrendRange } from "@/lib/api-types"
import { cn } from "@/lib/utils"

const ranges: Array<{ value: ProfitTrendRange; label: string }> = [
  { value: "24h", label: "24 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
]

const profitLines = [
  { key: "sale_cny", label: "销售额", stroke: "var(--brand)" },
  { key: "external_sales_cny", label: "对外销售额", stroke: "var(--chart-2)" },
  { key: "cost_cny", label: "上游成本", stroke: "var(--warning)" },
  { key: "profit_cny", label: "毛利润", stroke: "var(--success)" },
  { key: "personal_usage_cny", label: "自用金额", stroke: "var(--danger)" },
  { key: "operating_profit_cny", label: "经营利润", stroke: "var(--chart-3)" },
] as const

type ProfitLineKey = (typeof profitLines)[number]["key"]
type ProfitLineVisibility = Record<ProfitLineKey, boolean>

function formatTick(iso: string, range: ProfitTrendRange) {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  if (range === "24h") return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false })
  return `${date.getMonth() + 1}/${date.getDate()}`
}

function formatYAxis(value: number) {
  if (value === 0) return "¥0"
  if (Math.abs(value) >= 1000) return `¥${(value / 1000).toFixed(1)}K`
  return `¥${value.toFixed(value >= 10 ? 0 : 2)}`
}

function ProfitTooltip({ active, payload, label, visibleKeys }: { active?: boolean; payload?: Array<{ dataKey: string; value?: number }>; label?: string; visibleKeys: Set<ProfitLineKey> }) {
  if (!active || !payload?.length) return null
  const visiblePayload = payload.filter((item) => visibleKeys.has(item.dataKey as ProfitLineKey) && typeof item.value === "number")
  if (!visiblePayload.length) return null
  return (
    <div className="min-w-40 rounded-md border border-border bg-popover px-3 py-2 shadow-lg">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="mt-1.5 space-y-1">
        {visiblePayload.map((item) => (
          <div key={item.dataKey} className="flex items-center justify-between gap-4 text-xs">
            <span className="text-muted-foreground">{profitLines.find((line) => line.key === item.dataKey)?.label ?? item.dataKey}</span>
            <span className="font-mono font-medium tabular-nums text-foreground">{cny(item.value, { precise: true })}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

export function ProfitOverview() {
  const [range, setRange] = useState<ProfitTrendRange>("24h")
  const [detailKind, setDetailKind] = useState<ProfitDetailKind>("reconciliation")
  const [detailPage, setDetailPage] = useState(1)
  const [visibleLines, setVisibleLines] = useState<ProfitLineVisibility>({
    sale_cny: true,
    external_sales_cny: true,
    cost_cny: true,
    profit_cny: true,
    personal_usage_cny: true,
    operating_profit_cny: true,
  })
  const trend = useProfitTrend(range)
  const details = useProfitDetails(detailKind, range, detailPage, 25)
  const summary = trend.data?.summary
  const chartData = useMemo(
    () => (trend.data?.points ?? []).map((point) => ({
      ...point,
      // Keep the dashboard usable while an older API response is still cached.
      external_sales_cny: point.external_sales_cny ?? point.sale_cny - (point.personal_usage_cny ?? 0),
      operating_profit_cny: point.operating_profit_cny ?? point.net_profit_cny ?? point.profit_cny,
      label: formatTick(point.start_at, range),
    })),
    [range, trend.data],
  )
  const hasData = chartData.some((point) => profitLines.some((line) => point[line.key] !== 0))
  const visibleLineKeys = useMemo(
    () => new Set(profitLines.filter((line) => visibleLines[line.key]).map((line) => line.key)),
    [visibleLines],
  )
  const allLinesHidden = visibleLineKeys.size === 0

  function toggleLine(key: ProfitLineKey) {
    setVisibleLines((current) => ({ ...current, [key]: !current[key] }))
  }

  function changeDetailKind(value: string) {
    if (value !== "sales" && value !== "cost" && value !== "unmapped" && value !== "reconciliation") return
    setDetailKind(value)
    setDetailPage(1)
  }

  return (
    <Card data-testid="profit-overview" className="border border-border shadow-none">
      <CardHeader className="gap-3 pb-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2 text-base font-semibold">
            <LineChartIcon className="size-4 text-brand" />
            {"收益结算"}
          </CardTitle>
          <div className="mt-2 grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3 lg:grid-cols-7">
            <Metric label="销售额" value={cny(summary?.sale_cny)} />
            <Metric label="对外销售额" value={cny(summary?.external_sales_cny ?? summary?.sale_cny)} />
            <Metric label="上游成本" value={cny(summary?.cost_cny)} />
            <Metric
              label="毛利润"
              value={cny(summary?.profit_cny)}
              valueClass={summary && summary.profit_cny < 0 ? "text-danger" : "text-success"}
            />
            <Metric label="自用金额" value={cny(summary?.personal_usage_cny)} valueClass="text-warning" />
            <Metric
              label="经营利润"
              value={cny(summary?.operating_profit_cny ?? summary?.net_profit_cny ?? summary?.profit_cny)}
              valueClass={summary && (summary.operating_profit_cny ?? summary.net_profit_cny ?? summary.profit_cny) < 0 ? "text-danger" : "text-success"}
            />
            <Metric label="毛利率" value={summary ? `${(summary.profit_margin * 100).toFixed(1)}%` : "—"} />
          </div>
        </div>
        <ToggleGroup
          type="single"
          variant="outline"
          size="sm"
          value={range}
          onValueChange={(value) => value && setRange(value as ProfitTrendRange)}
          className="shrink-0 self-start bg-muted/20"
          aria-label="收益统计范围"
        >
          {ranges.map((item) => (
            <ToggleGroupItem key={item.value} value={item.value} className="h-7 px-2.5 text-xs">
              {item.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
      </CardHeader>
      <CardContent>
        <div className="h-64 min-h-64 w-full">
          {trend.loading && !trend.data ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">{"加载中…"}</div>
          ) : trend.error ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-danger">{trend.error}</div>
          ) : allLinesHidden ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">{"所有线条已隐藏，可从下方图例重新启用"}</div>
          ) : !hasData ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">{"暂无收益结算数据，等待账单同步"}</div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={chartData} margin={{ top: 8, right: 12, left: 0, bottom: 2 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
                <XAxis dataKey="label" tickLine={false} axisLine={false} tick={{ fill: "var(--muted-foreground)", fontSize: 11 }} dy={8} />
                <YAxis tickLine={false} axisLine={false} width={52} tick={{ fill: "var(--muted-foreground)", fontSize: 11 }} tickFormatter={formatYAxis} />
                <Tooltip content={<ProfitTooltip visibleKeys={visibleLineKeys} />} cursor={{ stroke: "var(--border)", strokeDasharray: "4 4" }} />
                {profitLines.map((line) => visibleLines[line.key] ? (
                  <Line key={line.key} type="monotone" dataKey={line.key} name={line.label} stroke={line.stroke} strokeWidth={2} dot={false} isAnimationActive={false} />
                ) : null)}
              </LineChart>
            </ResponsiveContainer>
          )}
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-border pt-3" role="group" aria-label="图表图例">
          {profitLines.map((line) => {
            const visible = visibleLines[line.key]
            return (
              <button
                key={line.key}
                type="button"
                aria-pressed={visible}
                aria-label={`${line.label}，${visible ? "已显示" : "已隐藏"}`}
                onClick={() => toggleLine(line.key)}
                className={cn(
                  "inline-flex min-h-7 items-center gap-1.5 rounded-md border px-2 text-xs transition-colors",
                  visible ? "border-border bg-background text-foreground" : "border-transparent bg-muted/50 text-muted-foreground line-through",
                )}
              >
                <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: line.stroke }} aria-hidden="true" />
                {line.label}
              </button>
            )
          })}
        </div>
        {summary ? (
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
            <span className="inline-flex items-center gap-1.5"><CircleDollarSign className="size-3.5 text-success" />{"已结算 "}{cny(summary.settled_sale_cny)}</span>
            {summary.unmapped_sale_cny > 0 ? <span className="inline-flex items-center gap-1.5 text-warning"><AlertCircle className="size-3.5" />{"未映射 "}{cny(summary.unmapped_sale_cny)}</span> : null}
            {summary.unsettled_sale_cny > 0 ? <span className="inline-flex items-center gap-1.5 text-warning">{"待补偿 "}{cny(summary.unsettled_sale_cny)}</span> : null}
            {!summary.complete ? <span className={cn("text-warning")}>{"毛利润数据尚未完整"}</span> : null}
            {summary.personal_usage_cny !== undefined && !summary.personal_usage_complete ? <span className="text-warning">{"自用金额数据尚未完整"}</span> : null}
          </div>
        ) : null}

        <Tabs value={detailKind} onValueChange={changeDetailKind} className="mt-4 border-t border-border pt-3">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex min-w-0 items-center gap-2">
              <ReceiptText className="size-4 shrink-0 text-brand" />
              <div className="min-w-0">
                <p className="text-sm font-medium text-foreground">收益明细</p>
                <p className="truncate text-[11px] text-muted-foreground">按上游成本桶核对销售额、成本与利润</p>
              </div>
            </div>
            <TabsList className="h-8 w-full justify-start overflow-x-auto rounded-md bg-muted/40 p-0.5 sm:w-auto">
              <TabsTrigger value="reconciliation" className="h-7 px-2.5 text-xs">对账</TabsTrigger>
              <TabsTrigger value="sales" className="h-7 px-2.5 text-xs">销售明细</TabsTrigger>
              <TabsTrigger value="cost" className="h-7 px-2.5 text-xs">成本明细</TabsTrigger>
              <TabsTrigger value="unmapped" className="h-7 px-2.5 text-xs">未映射</TabsTrigger>
            </TabsList>
          </div>

          <TabsContent value="reconciliation" className="mt-3">
            <ReconciliationDetail data={details.data?.reconciliation} loading={details.loading} error={details.error} />
          </TabsContent>
          <TabsContent value="sales" className="mt-3">
            <SalesDetailTable data={(details.data?.items ?? []) as ProfitSaleDetail[]} loading={details.loading} error={details.error} />
          </TabsContent>
          <TabsContent value="cost" className="mt-3">
            <CostDetailTable data={(details.data?.items ?? []) as ProfitCostDetail[]} loading={details.loading} error={details.error} />
          </TabsContent>
          <TabsContent value="unmapped" className="mt-3">
            <SalesDetailTable data={(details.data?.items ?? []) as ProfitSaleDetail[]} loading={details.loading} error={details.error} unmapped />
          </TabsContent>

          {detailKind !== "reconciliation" && details.data ? (
            <DetailPager
              page={details.data.page}
              pageSize={details.data.page_size}
              total={details.data.total}
              hasMore={details.data.has_more}
              onPageChange={setDetailPage}
            />
          ) : null}
        </Tabs>
      </CardContent>
    </Card>
  )
}

function Metric({ label, value, valueClass }: { label: string; value: string; valueClass?: string }) {
  return (
    <div className="min-w-0">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className={cn("mt-0.5 truncate text-sm font-semibold tabular-nums text-foreground", valueClass)}>{value}</p>
    </div>
  )
}

function DetailState({ loading, error, empty, children }: { loading: boolean; error: string | null; empty: boolean; children: React.ReactNode }) {
  if (loading && empty) {
    return <div className="flex min-h-24 items-center justify-center gap-2 text-xs text-muted-foreground"><LoaderCircle className="size-3.5 animate-spin" />加载明细中…</div>
  }
  if (error && empty) {
    return <div className="flex min-h-24 items-center justify-center px-4 text-center text-xs text-danger">{error}</div>
  }
  if (empty) {
    return <div className="flex min-h-24 items-center justify-center px-4 text-center text-xs text-muted-foreground">当前时间范围没有可核对的明细</div>
  }
  return <>{children}</>
}

function ReconciliationDetail({ data, loading, error }: { data?: ProfitReconciliation; loading: boolean; error: string | null }) {
  const empty = !data
  return (
    <DetailState loading={loading} error={error} empty={empty}>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-7">
        <DetailMetric label="销售额" value={cny(data?.sales_cny, { precise: true })} />
        <DetailMetric label="对外销售额" value={cny(data?.external_sales_cny ?? data?.sales_cny, { precise: true })} />
        <DetailMetric label="阶段成本" value={cny(data?.stage_usage_cost_cny, { precise: true })} />
        <DetailMetric label="毛利润" value={cny(data?.profit_cny, { precise: true })} valueClass={data && data.profit_cny < 0 ? "text-danger" : "text-success"} />
        <DetailMetric label="自用金额" value={cny(data?.personal_usage_cny, { precise: true })} valueClass="text-warning" />
        <DetailMetric label="经营利润" value={cny(data?.operating_profit_cny ?? data?.net_profit_cny ?? data?.profit_cny, { precise: true })} valueClass={data && (data.operating_profit_cny ?? data.net_profit_cny ?? data.profit_cny) < 0 ? "text-danger" : "text-success"} />
        <DetailMetric label="对账差额" value={cny(data?.reconciliation_delta_cny, { precise: true })} />
      </div>
      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        <span>销售明细 {cny(data?.sales_detail_cny, { precise: true })}</span>
        <span>成本明细 {cny(data?.cost_detail_cny, { precise: true })}</span>
        <span>已分摊成本 {cny(data?.allocated_cost_cny, { precise: true })}</span>
        <span>未匹配成本 {cny(data?.unmatched_cost_cny, { precise: true })}</span>
        <span>未映射销售 {cny(data?.unmapped_sales_cny, { precise: true })}</span>
        <Badge variant="outline" className={data?.complete ? "text-success" : "text-warning"}>{data?.complete ? "毛利润完整" : "毛利润待补齐"}</Badge>
        <Badge variant="outline" className={data?.personal_usage_complete ? "text-success" : "text-warning"}>{data?.personal_usage_complete ? "自用金额完整" : "自用金额待补齐"}</Badge>
        <Badge variant="outline" className={data?.net_profit_complete ? "text-success" : "text-warning"}>{data?.net_profit_complete ? "经营利润完整" : "经营利润待补齐"}</Badge>
      </div>
    </DetailState>
  )
}

function SalesDetailTable({ data, loading, error, unmapped = false }: { data: ProfitSaleDetail[]; loading: boolean; error: string | null; unmapped?: boolean }) {
  return (
    <DetailState loading={loading} error={error} empty={data.length === 0}>
      <div className="overflow-hidden rounded-md border border-border">
        <Table className="min-w-[760px] text-xs">
          <TableHeader>
            <TableRow>
              <TableHead>时间桶</TableHead>
              <TableHead>上游渠道</TableHead>
              <TableHead>NewAPI 渠道</TableHead>
              <TableHead>分组 / 模型</TableHead>
              <TableHead className="text-right">阶段成本</TableHead>
              <TableHead className="text-right">销售额</TableHead>
              <TableHead className="text-right">利润</TableHead>
              <TableHead>映射</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((item, index) => (
              <TableRow key={`${item.bucket_start}-${item.channel_id}-${index}`}>
                <TableCell className="font-mono text-[11px] text-muted-foreground">{formatBucket(item.bucket_start, item.bucket_end)}</TableCell>
                <TableCell>
                  <div className="font-medium text-foreground">{item.upstream_channel_name || "未匹配上游"}</div>
                  <div className="font-mono text-[11px] text-muted-foreground">{item.upstream_channel_id ? `#${item.upstream_channel_id}` : "—"}</div>
                </TableCell>
                <TableCell>
                  <div className="font-medium text-foreground">{item.newapi_channel_name || "未命名渠道"}</div>
                  <div className="font-mono text-[11px] text-muted-foreground">{item.newapi_channel_id ? `#${item.newapi_channel_id}` : "—"}</div>
                </TableCell>
                <TableCell>
                  <div className="max-w-36 truncate text-foreground" title={item.group}>{item.group || "—"}</div>
                  <div className="max-w-36 truncate text-[11px] text-muted-foreground" title={item.model_name}>{item.model_name || "—"}</div>
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums">{cny(item.cost_cny, { precise: true })}</TableCell>
                <TableCell className="text-right font-mono font-medium tabular-nums">{cny(item.sale_cny, { precise: true })}</TableCell>
                <TableCell className={cn("text-right font-mono font-medium tabular-nums", (item.profit_cny ?? 0) < 0 ? "text-danger" : "text-success")}>{cny(item.profit_cny, { precise: true })}</TableCell>
                <TableCell>
                  <MappingBadge status={item.mapping_status} forcedUnmapped={unmapped} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </DetailState>
  )
}

function CostDetailTable({ data, loading, error }: { data: ProfitCostDetail[]; loading: boolean; error: string | null }) {
  return (
    <DetailState loading={loading} error={error} empty={data.length === 0}>
      <div className="overflow-hidden rounded-md border border-border">
        <Table className="min-w-[650px] text-xs">
          <TableHeader>
            <TableRow>
              <TableHead>时间</TableHead>
              <TableHead>上游渠道</TableHead>
              <TableHead>阶段用量</TableHead>
              <TableHead className="text-right">成本 CNY</TableHead>
              <TableHead>来源 / 质量</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((item, index) => (
              <TableRow key={`${item.channel_id}-${item.bucket_start}-${index}`}>
                <TableCell className="font-mono text-[11px] text-muted-foreground">{formatTimestamp(item.bucket_start)}</TableCell>
                <TableCell>
                  <div className="font-medium text-foreground">{item.channel_name || "未命名渠道"}</div>
                  <div className="font-mono text-[11px] text-muted-foreground">#{item.channel_id}</div>
                </TableCell>
                <TableCell className="font-mono tabular-nums">{formatAmount(item.amount, item.currency)}</TableCell>
                <TableCell className="text-right font-mono font-medium tabular-nums">{cny(item.cost_cny, { precise: true })}</TableCell>
                <TableCell>
                  <div className="text-foreground">{item.source || "—"}</div>
                  <div className="text-[11px] text-muted-foreground">{item.quality || "—"}{item.complete ? " · 完整" : " · 待补齐"}</div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </DetailState>
  )
}

function DetailPager({ page, pageSize, total, hasMore, onPageChange }: { page: number; pageSize: number; total: number; hasMore: boolean; onPageChange: (page: number) => void }) {
  if (total <= pageSize && page <= 1) return null
  const first = total === 0 ? 0 : (page - 1) * pageSize + 1
  const last = Math.min(page * pageSize, total)
  return (
    <div className="mt-2 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
      <span>{first}-{last} / {total} 条</span>
      <div className="flex items-center gap-1">
        <Button type="button" variant="ghost" size="icon-sm" aria-label="上一页" disabled={page <= 1} onClick={() => onPageChange(Math.max(1, page - 1))}>
          <ChevronLeft />
        </Button>
        <span className="min-w-12 text-center">第 {page} 页</span>
        <Button type="button" variant="ghost" size="icon-sm" aria-label="下一页" disabled={!hasMore} onClick={() => onPageChange(page + 1)}>
          <ChevronRight />
        </Button>
      </div>
    </div>
  )
}

function DetailMetric({ label, value, valueClass }: { label: string; value: string; valueClass?: string }) {
  return <div><p className="text-[11px] text-muted-foreground">{label}</p><p className={cn("mt-0.5 font-mono text-sm font-semibold tabular-nums", valueClass ?? "text-foreground")}>{value}</p></div>
}

function MappingBadge({ status, forcedUnmapped }: { status: string; forcedUnmapped?: boolean }) {
  const normalized = forcedUnmapped ? "unmapped" : status
  const label = normalized === "mapped" ? "已映射" : normalized === "ambiguous" ? "映射歧义" : "未映射"
  const className = normalized === "mapped" ? "text-success" : normalized === "ambiguous" ? "text-warning" : "text-danger"
  return <Badge variant="outline" className={className}>{label}</Badge>
}

function formatTimestamp(value?: string) {
  if (!value) return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "—"
  return date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false })
}

function formatBucket(start?: string, end?: string) {
  const startDate = start ? new Date(start) : null
  const endDate = end ? new Date(end) : null
  if (!startDate || Number.isNaN(startDate.getTime())) return "—"
  const format = (date: Date) => date.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false })
  if (!endDate || Number.isNaN(endDate.getTime())) return format(startDate)
  return `${format(startDate)} - ${endDate.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false })}`
}

function formatAmount(value: number, currency: string) {
  if (!Number.isFinite(value)) return "—"
  return `${value.toLocaleString("en-US", { maximumFractionDigits: 4 })} ${currency || "USD"}`
}

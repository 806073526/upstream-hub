import type { UsageTrendRange, UsageTrendResponse } from "@/lib/api-types"

const BUSINESS_TIME_ZONE = "Asia/Shanghai"

interface DateParts {
  year: string
  month: string
  day: string
  hour: string
  minute: string
}

function dateParts(iso: string): DateParts | null {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return null
  const parts = new Intl.DateTimeFormat("zh-CN", {
    timeZone: BUSINESS_TIME_ZONE,
    year: "numeric",
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date)
  const value = (type: Intl.DateTimeFormatPartTypes) => parts.find((part) => part.type === type)?.value ?? ""
  return { year: value("year"), month: value("month"), day: value("day"), hour: value("hour"), minute: value("minute") }
}

export type UsageChartRow = {
  startAt: string
  endAt: string
  totalAmount: number | null
  selectedTotalAmount?: number | null
  quality: string
  complete: boolean
  missingChannelIDs: number[]
} & Record<string, string | number | boolean | number[] | null>

export function channelDataKey(channelID: number) {
  return `channel_${channelID}`
}

export function buildUsageChartData(response: UsageTrendResponse): UsageChartRow[] {
  return response.points.map((point) => {
    const row: UsageChartRow = {
      startAt: point.start_at,
      endAt: point.end_at,
      totalAmount: point.total_amount,
      quality: point.quality,
      complete: point.complete,
      missingChannelIDs: point.missing_channel_ids,
    }

    for (const channel of response.channels) {
      const key = String(channel.id)
      row[channelDataKey(channel.id)] = Object.prototype.hasOwnProperty.call(point.channel_amounts, key)
        ? point.channel_amounts[key]
        : null
    }
    return row
  })
}

export interface FilteredUsageChart {
  channels: UsageTrendResponse["channels"]
  rows: UsageChartRow[]
  rangeTotalAmount: number | null
}

export function buildFilteredUsageChart(
  response: UsageTrendResponse,
  disabledChannelIDs: ReadonlySet<number>,
): FilteredUsageChart {
  const channels = response.channels.filter((channel) => !disabledChannelIDs.has(channel.id))
  const rows = buildUsageChartData(response).map((row) => {
    if (channels.length === 0) return { ...row, selectedTotalAmount: 0 }

    let hasData = false
    let selectedTotalAmount = 0
    for (const channel of channels) {
      const amount = row[channelDataKey(channel.id)]
      if (typeof amount === "number") {
        hasData = true
        selectedTotalAmount += amount
      }
    }
    return { ...row, selectedTotalAmount: hasData ? selectedTotalAmount : null }
  })

  if (channels.length === 0) return { channels, rows, rangeTotalAmount: 0 }

  let hasRangeData = false
  let rangeTotalAmount = 0
  for (const row of rows) {
    if (typeof row.selectedTotalAmount === "number") {
      hasRangeData = true
      rangeTotalAmount += row.selectedTotalAmount
    }
  }
  return { channels, rows, rangeTotalAmount: hasRangeData ? rangeTotalAmount : null }
}

export function formatUsageTick(iso: string, range: UsageTrendRange): string {
  const parts = dateParts(iso)
  if (!parts) return iso
  if (range === "6m" || range === "1y") return `${parts.year}/${parts.month}`
  if (range === "30d") return `${parts.month}/${parts.day}`
  if (range === "7d") return `${parts.month}/${parts.day} ${parts.hour}:00`
  if (range === "1h") return `${parts.hour}:${parts.minute}`
  return `${parts.hour}:00`
}

export function formatUsageInterval(startISO: string, endISO: string, range: UsageTrendRange): string {
  const start = dateParts(startISO)
  const end = dateParts(endISO)
  if (!start || !end) return startISO
  if (range === "6m" || range === "1y") return `${start.year}年${start.month}月`
  if (range === "30d") return `${start.month}月${start.day}日`
  const dateLabel = `${start.month}月${start.day}日`
  return `${dateLabel} ${start.hour}:${start.minute}–${end.hour}:${end.minute}`
}

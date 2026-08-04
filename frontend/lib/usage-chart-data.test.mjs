import assert from "node:assert/strict"
import test from "node:test"
import {
  buildFilteredUsageChart,
  buildUsageChartData,
  channelDataKey,
  formatUsageInterval,
  formatUsageTick,
} from "./usage-chart-data.ts"

const response = {
  range: "1h",
  start_at: "2026-07-29T01:00:00Z",
  end_at: "2026-07-29T02:00:00Z",
  source_resolution_seconds: 300,
  output_resolution_seconds: 300,
  currency: "USD",
  channels: [
    { id: 7, name: "Alpha", type: "newapi", currency: "USD" },
    { id: 9, name: "Beta", type: "sub2api", currency: "USD" },
  ],
  points: [
    {
      start_at: "2026-07-29T01:00:00Z",
      end_at: "2026-07-29T01:05:00Z",
      total_amount: null,
      channel_amounts: {},
      quality: "missing",
      complete: false,
      missing_channel_ids: [7, 9],
    },
    {
      start_at: "2026-07-29T01:05:00Z",
      end_at: "2026-07-29T01:10:00Z",
      total_amount: 1.25,
      channel_amounts: { "7": 1.25 },
      quality: "exact",
      complete: false,
      missing_channel_ids: [9],
    },
    {
      start_at: "2026-07-29T01:10:00Z",
      end_at: "2026-07-29T01:15:00Z",
      total_amount: 0,
      channel_amounts: { "7": 0, "9": 0 },
      quality: "exact",
      complete: true,
      missing_channel_ids: [],
    },
  ],
  range_total_amount: 1.25,
  channel_totals: { "7": 1.25 },
  complete: false,
}

test("buildUsageChartData leaves missing intervals and channels empty", () => {
  const rows = buildUsageChartData(response)

  assert.equal(rows[0].totalAmount, null)
  assert.equal(rows[0][channelDataKey(7)], null)
  assert.equal(rows[0][channelDataKey(9)], null)
  assert.equal(rows[1][channelDataKey(7)], 1.25)
  assert.equal(rows[1][channelDataKey(9)], null)
})

test("buildUsageChartData preserves observed zero usage", () => {
  const rows = buildUsageChartData(response)

  assert.equal(rows[2].totalAmount, 0)
  assert.equal(rows[2][channelDataKey(7)], 0)
  assert.equal(rows[2][channelDataKey(9)], 0)
})

test("buildFilteredUsageChart excludes disabled channels from bars and totals", () => {
  const filtered = buildFilteredUsageChart(response, new Set([9]))

  assert.deepEqual(filtered.channels.map((channel) => channel.id), [7])
  assert.equal(filtered.rangeTotalAmount, 1.25)
  assert.equal(filtered.rows[0].selectedTotalAmount, null)
  assert.equal(filtered.rows[1].selectedTotalAmount, 1.25)
  assert.equal(filtered.rows[2].selectedTotalAmount, 0)
})

test("buildFilteredUsageChart reports zero when every channel is disabled", () => {
  const filtered = buildFilteredUsageChart(response, new Set([7, 9]))

  assert.deepEqual(filtered.channels, [])
  assert.equal(filtered.rangeTotalAmount, 0)
  assert.equal(filtered.rows[1].selectedTotalAmount, 0)
})

test("formatUsageTick matches the selected statistical granularity", () => {
  const instant = "2026-07-29T01:05:00Z"

  assert.equal(formatUsageTick(instant, "1h"), "09:05")
  assert.equal(formatUsageTick(instant, "today"), "09:00")
	assert.equal(formatUsageTick(instant, "7d"), "7/29 09:00")
	assert.equal(formatUsageTick(instant, "30d"), "7/29")
	assert.equal(formatUsageTick("2026-07-01T00:00:00Z", "6m"), "2026/7")
	assert.equal(formatUsageTick("2026-07-01T00:00:00Z", "1y"), "2026/7")
})

test("formatUsageInterval describes the actual half-open time bucket", () => {
  assert.equal(
    formatUsageInterval("2026-07-29T01:05:00Z", "2026-07-29T01:10:00Z", "1h"),
    "7月29日 09:05–09:10",
  )
	assert.equal(
		formatUsageInterval("2026-07-28T16:00:00Z", "2026-07-29T16:00:00Z", "30d"),
		"7月29日",
	)
	assert.equal(
		formatUsageInterval("2026-06-30T16:00:00Z", "2026-07-31T16:00:00Z", "6m"),
		"2026年7月",
	)
})

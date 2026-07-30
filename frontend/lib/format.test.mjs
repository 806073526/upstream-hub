import assert from "node:assert/strict"
import test from "node:test"
import { ratioArrow } from "./format.ts"

test("ratioArrow preserves three decimal places", () => {
  assert.equal(ratioArrow(0.035, 0.125), "0.035 → 0.125")
})

test("ratioArrow formats a missing previous ratio and whole-number ratio consistently", () => {
  assert.equal(ratioArrow(null, 1), "— → 1.000")
})

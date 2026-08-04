const SECOND_MS = 1000
const DAY_MS = 24 * 60 * 60 * 1000
const USAGE_RECORDS_RETENTION_WINDOW_MS = 7 * DAY_MS
const USAGE_RECORDS_REQUEST_MARGIN_MS = 2 * SECOND_MS

// Request timestamps are serialized without milliseconds. Round the client
// clock up before applying the margin so the wire value stays inside the
// server's whole-second rolling retention cutoff during normal request delay.
export function usageRecordsRetentionStart(now = Date.now()): number {
  const serializedNow = Math.ceil(now / SECOND_MS) * SECOND_MS
  return serializedNow - USAGE_RECORDS_RETENTION_WINDOW_MS + USAGE_RECORDS_REQUEST_MARGIN_MS
}

export function usageRecordsLatestRange(now = Date.now()): [number, number] {
  return [usageRecordsRetentionStart(now), now]
}

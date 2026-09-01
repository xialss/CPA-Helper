export type ModelMonitorHistoryStripGranularity = 'day' | 'minute'

export interface ModelMonitorHistoryStripItem<T> {
  key: string
  sample: T | null
}

const minuteHistoryWindowSize = 60

export function minuteHistoryStripItems<T extends { timestamp: string }>(
  samples: readonly T[],
  granularity: ModelMonitorHistoryStripGranularity,
): ModelMonitorHistoryStripItem<T>[] {
  const sampleItems = samples.map((sample, index) => ({
    key: `sample-${index}-${sample.timestamp}`,
    sample,
  }))

  if (granularity !== 'minute' || samples.length === 0 || samples.length >= minuteHistoryWindowSize) {
    return sampleItems
  }

  const emptyItems: ModelMonitorHistoryStripItem<T>[] = Array.from(
    { length: minuteHistoryWindowSize - samples.length },
    (_, index) => ({
      key: `empty-${index}`,
      sample: null,
    }),
  )
  return [...emptyItems, ...sampleItems]
}

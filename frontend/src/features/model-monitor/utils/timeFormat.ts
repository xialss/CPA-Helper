export type ModelMonitorSampleTimeGranularity = 'day' | 'minute'

export function modelMonitorTimestampFormatOptions(): Intl.DateTimeFormatOptions {
  return {
    dateStyle: 'medium',
    timeStyle: 'short',
  }
}

export function modelMonitorSampleTimeFormatOptions(
  granularity: ModelMonitorSampleTimeGranularity,
): Intl.DateTimeFormatOptions {
  if (granularity === 'day') {
    return {
      month: 'short',
      day: 'numeric',
      timeZone: 'UTC',
    }
  }
  return {
    hour: '2-digit',
    minute: '2-digit',
  }
}

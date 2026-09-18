/**
 * Scheduling for the AI radar's automatic refresh.
 *
 * The cadence is wall-clock aligned: the page reads again at :00, :05, :10 and
 * so on, rather than five minutes after it happened to be opened.
 */

/** Five minutes divides an hour exactly, so this stays clock-aligned. */
export const radarAutoRefreshIntervalMs = 5 * 60 * 1000

/**
 * Milliseconds from `now` until the next multiple of `intervalMs`, measured on
 * the epoch clock. A whole-minute timezone offset (Asia/Shanghai is +08:00)
 * makes epoch-aligned boundaries coincide with local :00 / :05 / :10.
 *
 * Returns a full interval when already exactly on a boundary, so a caller that
 * re-arms from this value can never produce a zero-delay loop.
 */
export function msUntilNextBoundary(
  now: number,
  intervalMs: number = radarAutoRefreshIntervalMs,
): number {
  if (!Number.isFinite(intervalMs) || intervalMs <= 0) {
    throw new RangeError('intervalMs must be a positive finite number')
  }
  const remainder = ((now % intervalMs) + intervalMs) % intervalMs
  return remainder === 0 ? intervalMs : intervalMs - remainder
}

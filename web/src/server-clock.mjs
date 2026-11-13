// Observations use Gateway timestamps. Calibrate against its HTTP Date header
// so the browser's wall clock cannot make a fresh result expire immediately.
export function createServerClock({ wallNow = Date.now, elapsedNow = () => performance.now() } = {}) {
  let anchor, received, latestRequest = -Infinity
  return {
    synchronize(date, started) {
      const serverTime = Date.parse(date)
      if (!Number.isFinite(serverTime) || started < latestRequest) return
      const ended = elapsedNow()
      // HTTP Date has second precision; use the middle of that second and
      // approximate the response's travel time with half the request duration.
      anchor = serverTime + 500 + Math.max(0, ended - started) / 2
      received = ended
      latestRequest = started
    },
    now() {
      return anchor === undefined ? wallNow() : anchor + elapsedNow() - received
    },
  }
}

// Match exactly the server's frozen cleanup preview. Running/queued jobs and
// commands still inside the replay-protection window are never eligible.
function timestamp(value) {
  const millis = Date.parse(value)
  if (!Number.isFinite(millis)) return undefined
  // Go uses nanosecond timestamps; Date.parse alone would round across the
  // preview/protection boundary when two jobs differ by less than 1ms.
  const fraction = /\.(\d+)(?:Z|[+-]\d\d:\d\d)$/.exec(value)?.[1] || ''
  return BigInt(millis) * 1000000n + BigInt(fraction.slice(3).padEnd(6, '0').slice(0, 6))
}
export function matchesHistoryCleanup(job, plan) {
  if (['pending', 'running'].includes(job.status)) return false
  if (!plan) return true // Partial cleanup: discard stale snapshots; allow a fresh read.
  const before = timestamp(plan.before), finished = timestamp(job.finished_at), expires = timestamp(job.expires_at)
  if (before === undefined || finished === undefined || expires === undefined) return false
  return finished <= before && expires < before - 60000000000n &&
    (plan.scope === 'completed' || finished < before - BigInt(plan.retention_days) * 86400000000000n)
}

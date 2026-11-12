const active = job => ['pending', 'running'].includes(job.status)
const finished = new Set(['success', 'partial', 'failed', 'timeout', 'cancelled', 'expired', 'interrupted'])

// Submit one device at a time and await its existing WakeJob. No separate wake
// engine, unlimited queue or automatic retry of an uncertain command is used.
export async function runBatch(rows, { submit, getJob, waitForJob, shouldStop = () => false, onUpdate = (_row) => {} }) {
  for (const row of rows) {
    if (finished.has(row.status)) continue
    if (shouldStop()) break
    try {
      row.status = row.job_id ? 'running' : 'submitting'
      row.message = row.job_id ? '正在查询已有任务' : '正在提交唤醒任务'
      onUpdate(row)
      let job
      if (row.job_id) job = await getJob(row.job_id)
      else {
        try { job = await submit(row.device_id, row.command_id) }
        catch (error) {
          if (error.code !== 'device_busy' || !error.jobID) throw error
          job = await getJob(error.jobID)
          // A cloned device may share a VNC target with another device. Do not
          // attribute that other device's job to this row or bypass its lock.
          if (job.device_id !== row.device_id) throw error
        }
      }
      if (job.device_id !== row.device_id) throw new Error('任务与所选设备不一致')
      row.job_id = job.job_id
      const update = job => {
        if (job.device_id !== row.device_id) throw new Error('任务与所选设备不一致')
        row.status = job.status
        row.message = job.message || (active(job) ? '任务正在后台执行' : '执行结束')
        onUpdate(row)
      }
      update(job)
      if (active(job)) update(await waitForJob(job, update))
    } catch (error) {
      const uncertain = !error.status || error.status >= 500
      const blocked = uncertain || [401, 403, 429].includes(error.status)
      row.status = blocked ? 'paused' : 'failed'
      row.message = error.message || '操作未完成'
      onUpdate(row)
      // Keep command_id and any known job_id. An explicit Continue reuses them
      // so an HTTP timeout cannot create another VNC authentication attempt.
      if (blocked) return { paused: true, message: row.message }
    }
  }
  return { paused: shouldStop() && rows.some(r => !finished.has(r.status)), message: shouldStop() ? '已停止后续提交；已创建的任务仍在后台执行' : '' }
}

export function batchFinished(status) { return finished.has(status) }

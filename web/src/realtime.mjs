import { matchesHistoryCleanup } from './history.mjs'

const active = job => ['pending', 'running'].includes(job.status)
const interrupted = () => Object.assign(new Error('实时连接已中断，已暂停后续提交；连接恢复后可继续未完成任务。'), { code: 'live_interrupted' })

// One stream and one task cache shared by cards, dialogs and batch execution.
// HTTP snapshots are accepted only if no newer event arrived during the read.
/** @param {{request: Function, createSource?: Function, onJob?: Function, onDevices?: Function, onStatus?: Function, onAuthExpired?: Function, onHistoryCleared?: Function, onMissing?: Function, getActiveIDs?: () => string[]}} options */
export function createRealtime({ request, createSource = url => new EventSource(url), onJob = (_job) => {}, onDevices = async (_revision) => {}, onStatus = (_status) => {}, onAuthExpired = () => {}, onHistoryCleared = (_plan, _ids) => {}, onMissing = (_id) => {}, getActiveIDs = () => [] }) {
  const records = new Map(), reads = new Map(), waiters = new Map()
  let source, status = 'closed', generation = 0, lifecycle = 0, clock = 0, stopped = true, fault = ''
  let authCheck, retryTimer, retryDelay = 3000, initialTimer, initialResolve, cleanupClock = 0
  function initialDone(ready) { clearTimeout(initialTimer); const resolve = initialResolve; initialResolve = undefined; resolve?.(ready) }
  const checkpoint = () => ({ generation, clock })
  const connected = () => status === 'connected' && !fault
  const values = () => [...records.values()].map(r => r.job).sort((a, b) => b.created_at?.localeCompare(a.created_at || '') || b.job_id.localeCompare(a.job_id))
  const setStatus = value => { status = value; onStatus(value) }
  function rejectWaiters(error) {
    for (const group of [...waiters.values()]) for (const waiter of [...group]) waiter.reject(error)
  }
  function remember(job, mark) {
    if (mark && mark.clock < cleanupClock && !active(job)) throw new Error('任务记录已发生变化，请重新查看')
    const previous = records.get(job.job_id)
    if (mark && (mark.generation !== generation || previous?.clock > mark.clock)) return previous?.job || job
    const copy = { ...job, steps: job.steps?.map(step => ({ ...step })), observation: { ...job.observation } }
    records.set(job.job_id, { job: copy, clock: ++clock })
    // Bound completed snapshots without evicting active tasks or task waiters.
    if (records.size > 256) {
      for (const old of [...records.keys()]) {
        if (records.size <= 256) break
        if (old !== job.job_id && !active(records.get(old).job) && !waiters.has(old)) records.delete(old)
      }
    }
    onJob(copy)
    for (const waiter of [...(waiters.get(copy.job_id) || [])]) {
      waiter.update(copy)
      if (copy.code === 'storage_unavailable') waiter.reject(new Error(copy.message))
      else if (!active(copy)) waiter.resolve(copy)
    }
    return copy
  }
  function readJob(id) {
    if (reads.has(id)) return reads.get(id)
    const mark = checkpoint()
    const pending = (async () => {
      let job
      try { job = await request('/wake-jobs/' + encodeURIComponent(id)) }
      catch (error) {
        if (error.status === 404 && mark.generation === generation) {
          forgetJob(id)
        }
        throw error
      }
      if (mark.generation !== generation) throw interrupted()
      return remember(job, mark)
    })().finally(() => { if (reads.get(id) === pending) reads.delete(id) })
    reads.set(id, pending)
    return pending
  }
  async function submit(id, key, action = 'wake') {
    const mark = checkpoint()
    const session = lifecycle
    const job = await request('/devices/' + id + '/' + action, { method: 'POST', headers: { 'Idempotency-Key': key } })
    if (session !== lifecycle || stopped) return job
    const accepted = remember(job, { ...mark, generation })
    if (mark.generation !== generation && connected()) return readJob(job.job_id)
    return accepted
  }
  function waitForJob(job, update = (_job) => {}) {
    const current = records.get(job.job_id)?.job || job
    update(current)
    if (!active(current)) return Promise.resolve(current)
    if (!connected()) return Promise.reject(fault ? new Error(fault) : interrupted())
    return new Promise((resolve, reject) => {
      const group = waiters.get(job.job_id) || new Set()
      let timer, settled = false
      const finish = (fn, value) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        group.delete(waiter)
        if (!group.size) waiters.delete(job.job_id)
        fn(value)
      }
      const waiter = { update, resolve: value => finish(resolve, value), reject: error => finish(reject, error) }
      group.add(waiter); waiters.set(job.job_id, group)
      // A deadline pauses this page's orchestration; it never cancels or retries
      // the server task and never starts periodic GET requests.
      timer = setTimeout(() => waiter.reject(new Error('等待任务结果超时，请刷新状态后继续确认已有任务。')), 180000)
      if (!records.has(job.job_id)) readJob(job.job_id).catch(waiter.reject)
    })
  }
  function overlayDevices(devices, mark) {
    const copies = devices.map(d => ({ ...d }))
    for (const record of [...records.values()].filter(r => r.clock > mark.clock).sort((a, b) => a.clock - b.clock)) {
      const d = copies.find(d => d.id === record.job.device_id)
      if (!d) continue
      if (active(record.job)) d.active_job_id = record.job.job_id
      else if (d.active_job_id === record.job.job_id) d.active_job_id = ''
    }
    return copies
  }
  async function synchronize() {
    const current = generation
    const known = [...records.values()].filter(r => active(r.job)).map(r => r.job.job_id)
    await onDevices(0)
    const ids = [...new Set([...known, ...getActiveIDs()])]
    let index = 0
    await Promise.all(Array.from({ length: Math.min(4, ids.length) }, async () => {
      while (index < ids.length) {
        if (current !== generation || stopped) throw interrupted()
        try { await readJob(ids[index++]) }
        catch (error) { if (error.status !== 404) throw error }
      }
    }))
    if (current !== generation || stopped) throw interrupted()
    if (status === 'stale') setStatus(fault ? 'blocked' : 'connected')
  }
  async function refresh() {
    // Manual reconciliation must work even if EventSource is unavailable.
    try { await synchronize() }
    catch (error) {
      if (source && status === 'connected') markStale()
      throw error
    }
    if (!source && !stopped) open()
  }
  function markStale() {
    generation++; reads.clear(); rejectWaiters(interrupted()); setStatus('stale')
  }
  async function checkSession() {
    if (authCheck || stopped) return
    const current = generation
    authCheck = (async () => {
      try {
        const session = await request('/auth/session')
        if (current === generation && !session.authenticated && !stopped) { stop(); onAuthExpired() }
      } catch { /* Network failure: EventSource handles reconnect, no list polling. */ }
    })().finally(() => { authCheck = undefined })
    return authCheck
  }
  function open() {
    if (stopped || source) return
    clearTimeout(retryTimer); retryTimer = undefined
    let stream
    try { stream = createSource('/api/v1/events') }
    catch { setStatus('unsupported'); initialDone(false); return }
    source = stream
    stream.addEventListener('resync', async event => {
      if (stopped || source !== stream) return
      const current = generation
      try {
        const data = JSON.parse(event.data)
        fault = data.ready === false ? 'Gateway 存储异常，请检查磁盘并重启服务。' : ''
        retryDelay = 3000
        setStatus('syncing')
        await synchronize()
        if (current === generation && !stopped) { setStatus(fault ? 'blocked' : 'connected'); initialDone(true) }
      } catch {
        if (current !== generation || stopped) return
        stream.close(); source = undefined; disconnect(); checkSession()
        retryTimer = setTimeout(open, retryDelay)
      }
    })
    stream.addEventListener('job.updated', event => {
      if (!stopped && source === stream) remember(JSON.parse(event.data).job)
    })
    stream.addEventListener('history.cleared', event => {
      if (!stopped && source === stream) clearHistory(JSON.parse(event.data).cleanup)
    })
    stream.addEventListener('history.invalidated', () => {
      if (!stopped && source === stream) clearHistory(null)
    })
    stream.addEventListener('history.removed', event => {
      if (stopped || source !== stream) return
      cleanupClock = ++clock
      for (const id of JSON.parse(event.data).job_ids) forgetJob(id)
    })
    stream.addEventListener('devices.changed', event => {
      if (stopped || source !== stream) return
      const current = generation
      Promise.resolve(onDevices(JSON.parse(event.data).revision)).catch(() => {
        if (current === generation && source === stream && !stopped) markStale()
      })
    })
    stream.addEventListener('storage.error', event => {
      if (stopped || source !== stream) return
      fault = JSON.parse(event.data).message || '存储写入失败，请检查磁盘并重启服务。'
      rejectWaiters(new Error(fault)); setStatus('blocked')
    })
    stream.addEventListener('auth.expired', () => { if (source === stream) { stop(); onAuthExpired() } })
    stream.onerror = () => {
      if (stopped || source !== stream) return
      disconnect(); checkSession()
      // Most transport failures reconnect inside EventSource. A terminal HTTP
      // failure (e.g. connection limit) needs a bounded delayed reopen instead.
      if (stream.readyState === 2) {
        stream.close(); source = undefined
        clearTimeout(retryTimer)
        retryTimer = setTimeout(open, retryDelay)
        retryDelay = Math.min(30000, retryDelay * 2)
      }
    }
  }
  function disconnect() {
    generation++; reads.clear(); rejectWaiters(interrupted()); setStatus('disconnected'); initialDone(false)
  }
  function start() {
    stop(); stopped = false; fault = ''; setStatus('connecting')
    const ready = new Promise(resolve => { initialResolve = resolve; initialTimer = setTimeout(() => initialDone(false), 2500) })
    open(); return ready
  }
  function stop() {
    initialDone(false)
    stopped = true; generation++; lifecycle++; source?.close(); source = undefined
    clearTimeout(retryTimer); reads.clear(); rejectWaiters(interrupted()); setStatus('closed')
  }
  function clear() { records.clear(); clock++; fault = '' }
  function forgetJob(id) {
    records.delete(id)
    for (const waiter of [...(waiters.get(id) || [])]) waiter.reject(new Error('任务记录已清理，无法确认结果，请查看历史记录'))
    onMissing(id)
  }
  function clearHistory(plan) {
    cleanupClock = ++clock
    const ids = []
    for (const [id, record] of records) {
      if (matchesHistoryCleanup(record.job, plan)) { records.delete(id); ids.push(id) }
    }
    onHistoryCleared(plan, ids)
  }
  const newer = (job, mark) => records.get(job.job_id)?.clock > mark.clock ? records.get(job.job_id).job : job
  return { start, stop, clear, clearHistory, checkpoint, remember, newer, readJob, submit, waitForJob, synchronize, refresh, overlayDevices, connected, values,
    get generation() { return generation } }
}

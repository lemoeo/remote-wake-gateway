let csrf = ''
export function setCSRF(value) { csrf = value || '' }
export class APIError extends Error {
  constructor(status, detail) { super(detail?.message || `请求失败 (${status})`); this.status = status; this.code = detail?.code; this.jobID = detail?.job_id }
}
// A timed-out mutation must keep its original idempotency key; never retry here.
export async function request(path, options = {}) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 10000)
  try {
    const response = await fetch('/api/v1' + path, { credentials: 'same-origin', ...options, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf, ...options.headers }, signal: controller.signal })
    if (response.status === 204) return null
    const body = await response.json()
    if (!response.ok) throw new APIError(response.status, body.error)
    return body
  } catch (error) {
    if (error?.name === 'AbortError') throw new Error('请求超时；操作可能已执行，请查询任务结果。')
    throw error
  } finally { clearTimeout(timer) }
}
export function commandKey() {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return 'cmd_' + Array.from(bytes, x => x.toString(16).padStart(2, '0')).join('')
}

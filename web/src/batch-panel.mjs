import { computed, onScopeDispose, ref, watch } from 'vue'
import { batchFinished } from './batch.mjs'

export function useBatchPanel(rows, running) {
  const visible = ref(false), collapsed = ref(false)
  const summary = computed(() => {
    const total = rows.value.length
    const completed = rows.value.filter(row => batchFinished(row.status)).length
    const success = rows.value.filter(row => row.status === 'success').length
    return { total, completed, success, issues: completed - success, allSuccessful: total > 0 && success === total }
  })
  let timer
  function clearTimer() { if (timer !== undefined) clearTimeout(timer); timer = undefined }
  watch(() => !running.value && summary.value.allSuccessful, ready => {
    clearTimer()
    if (ready && visible.value && !collapsed.value) {
      timer = setTimeout(() => { collapsed.value = true; timer = undefined }, 5000)
    }
  }, { flush: 'sync' })
  function reset() { clearTimer(); visible.value = true; collapsed.value = false }
  function toggle() { clearTimer(); collapsed.value = !collapsed.value }
  function close() {
    if (running.value || summary.value.completed !== summary.value.total) return
    clearTimer(); visible.value = false
  }
  onScopeDispose(clearTimer)
  return { visible, collapsed, summary, reset, toggle, close }
}

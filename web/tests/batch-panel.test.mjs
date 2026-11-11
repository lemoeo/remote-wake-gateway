import test from 'node:test'
import assert from 'node:assert/strict'
import { effectScope, ref } from 'vue'
import { useBatchPanel } from '../src/batch-panel.mjs'

function setup(t) {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const scope = effectScope(), rows = ref([{ status: 'pending' }]), running = ref(true)
  const panel = scope.run(() => useBatchPanel(rows, running))
  panel.reset()
  t.after(() => scope.stop())
  return { panel, rows, running, scope }
}

test('only a fully successful, stopped batch collapses after five seconds', t => {
  const { panel, rows, running } = setup(t)
  rows.value[0].status = 'success'
  t.mock.timers.tick(6000)
  assert.equal(panel.collapsed.value, false)
  running.value = false
  t.mock.timers.tick(4999)
  assert.equal(panel.collapsed.value, false)
  t.mock.timers.tick(1)
  assert.equal(panel.collapsed.value, true)
  panel.toggle()
  t.mock.timers.tick(10000)
  assert.equal(panel.collapsed.value, false)
})

test('partial, failed and paused results stay expanded and count correctly', t => {
  const { panel, rows, running } = setup(t)
  rows.value = ['success', 'partial', 'failed', 'paused'].map(status => ({ status }))
  running.value = false
  t.mock.timers.tick(10000)
  assert.equal(panel.collapsed.value, false)
  assert.deepEqual(panel.summary.value, { total: 4, completed: 3, success: 1, issues: 2, allSuccessful: false })
  panel.close()
  assert.equal(panel.visible.value, true)
})

test('manual toggle and a new batch cancel the old collapse timer', t => {
  const { panel, rows, running } = setup(t)
  rows.value[0].status = 'success'; running.value = false
  panel.toggle(); panel.toggle()
  t.mock.timers.tick(6000)
  assert.equal(panel.collapsed.value, false)
  rows.value = [{ status: 'pending' }]; running.value = true
  panel.reset()
  rows.value[0].status = 'success'; running.value = false
  t.mock.timers.tick(2000)
  panel.reset(); rows.value = [{ status: 'running' }]; running.value = true
  t.mock.timers.tick(6000)
  assert.equal(panel.collapsed.value, false)
  assert.equal(panel.visible.value, true)
})

test('close only hides completed feedback, and disposal cancels its timer', t => {
  const { panel, rows, running, scope } = setup(t)
  panel.close()
  assert.equal(panel.visible.value, true)
  rows.value[0].status = 'failed'; running.value = false
  panel.close()
  assert.equal(panel.visible.value, false)
  assert.equal(rows.value[0].status, 'failed')
  rows.value = [{ status: 'pending' }]; running.value = true; panel.reset()
  rows.value[0].status = 'success'; running.value = false
  scope.stop(); t.mock.timers.tick(6000)
  assert.equal(panel.collapsed.value, false)
})

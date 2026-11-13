import test from 'node:test'
import assert from 'node:assert/strict'
import { createServerClock } from '../src/server-clock.mjs'
import { observationLabel } from '../src/job-presentation.mjs'

const serverTime = Date.parse('2026-11-13T06:00:00Z')
const observation = { state: 'reachable', observed_at: new Date(serverTime).toISOString() }

test('fresh observations work when the browser clock is ahead or behind the Gateway', () => {
  for (const skew of [-86400000, 3600000, 86400000]) {
    let elapsed = 100
    const clock = createServerClock({ wallNow: () => serverTime + skew, elapsedNow: () => elapsed })
    clock.synchronize(new Date(serverTime).toUTCString(), 100)
    assert.equal(observationLabel(observation, clock.now()), '端口可达')
    assert.equal(observationLabel({ ...observation, state: 'service_ready' }, clock.now()), 'VNC 已验证')
    elapsed += 58000
    assert.equal(observationLabel(observation, clock.now()), '端口可达')
    elapsed += 2000
    assert.equal(observationLabel(observation, clock.now()), '状态未知')
  }
})

test('calibrated expiry uses elapsed time even if the browser wall clock changes', () => {
  let wall = serverTime, elapsed = 0
  const clock = createServerClock({ wallNow: () => wall, elapsedNow: () => elapsed })
  clock.synchronize(new Date(serverTime).toUTCString(), 0)
  wall += 86400000
  elapsed = 1000
  assert.equal(observationLabel(observation, clock.now()), '端口可达')
  wall -= 172800000
  elapsed = 61000
  assert.equal(observationLabel(observation, clock.now()), '状态未知')
})

test('invalid Date headers and older in-flight responses cannot replace a valid calibration', () => {
  let elapsed = 100
  const clock = createServerClock({ wallNow: () => 123, elapsedNow: () => elapsed })
  clock.synchronize(undefined, 0)
  assert.equal(clock.now(), 123)
  clock.synchronize(new Date(serverTime).toUTCString(), 100)
  elapsed = 200
  const now = clock.now()
  clock.synchronize('invalid', 150)
  clock.synchronize(new Date(serverTime - 3600000).toUTCString(), 50)
  assert.equal(clock.now(), now)
})

test('old, missing and invalid observations never imply a reachable target', () => {
  assert.equal(observationLabel(observation, serverTime + 60000), '状态未知')
  for (const observed_at of [undefined, 'invalid', '0001-01-01T00:00:00Z']) {
    assert.equal(observationLabel({ state: 'reachable', observed_at }, serverTime), '状态未知')
  }
  assert.equal(observationLabel(undefined, serverTime), '状态未知')
  assert.equal(observationLabel({ ...observation, state: 'unknown' }, serverTime), '状态未知')
})

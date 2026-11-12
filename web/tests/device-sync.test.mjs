import test from 'node:test'
import assert from 'node:assert/strict'
import { createDeviceSync } from '../src/device-sync.mjs'

test('coalesced invalidations await the highest revision and propagate failure',async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  const loads=[];const sync=createDeviceSync({load:async revision=>{loads.push(revision);throw Error('device snapshot failed')}})
  const first=sync.schedule(2),second=sync.schedule(3)
  assert.equal(first,second)
  const failed=assert.rejects(first,/device snapshot failed/)
  t.mock.timers.tick(60);await failed
  assert.deepEqual(loads,[3])
})
test('session teardown resolves pending invalidations without fetching old data',async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  const loads=[];const sync=createDeviceSync({load:async revision=>loads.push(revision)})
  const pending=sync.schedule(2);sync.cancel();t.mock.timers.tick(1000)
  await pending;assert.deepEqual(loads,[])
})

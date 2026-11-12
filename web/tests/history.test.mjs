import test from 'node:test'
import assert from 'node:assert/strict'
import { matchesHistoryCleanup } from '../src/history.mjs'

const before=Date.parse('2026-11-12T00:00:00Z')
const date=n=>new Date(n).toISOString()
const plan={scope:'expired',before:date(before),retention_days:7}
const old={status:'success',finished_at:date(before-8*86400000),expires_at:date(before-8*86400000)}
test('cleanup matches server retention and never removes active or protected jobs',()=>{
  assert.equal(matchesHistoryCleanup(old,plan),true)
  for(const status of ['pending','running'])assert.equal(matchesHistoryCleanup({...old,status},plan),false)
  assert.equal(matchesHistoryCleanup({...old,expires_at:date(before-60000)},plan),false)
  assert.equal(matchesHistoryCleanup({...old,finished_at:date(before-7*86400000)},plan),false)
  assert.equal(matchesHistoryCleanup({...old,finished_at:date(before-86400000)},plan),false)
  assert.equal(matchesHistoryCleanup({...old,finished_at:date(before-86400000)},{...plan,scope:'completed'}),true)
  assert.equal(matchesHistoryCleanup({...old,finished_at:date(before+1)},{...plan,scope:'completed'}),false)
})
test('partial cleanup invalidates terminal snapshots but retains active jobs',()=>{
  assert.equal(matchesHistoryCleanup(old,null),true)
  assert.equal(matchesHistoryCleanup({...old,status:'running'},null),false)
  assert.equal(matchesHistoryCleanup({status:'success'},plan),false)
})
test('nanosecond precision preserves the exact server preview boundary',()=>{
  const precise={...plan,scope:'completed',before:'2026-11-12T00:00:00.123456789Z'}
  assert.equal(matchesHistoryCleanup({...old,finished_at:'2026-11-12T00:00:00.123456790Z'},precise),false)
  assert.equal(matchesHistoryCleanup({...old,finished_at:'2026-11-12T00:00:00.123456789Z'},precise),true)
  assert.equal(matchesHistoryCleanup({...old,expires_at:'2026-11-11T23:59:00.123456789Z'},precise),false)
  assert.equal(matchesHistoryCleanup({...old,expires_at:'2026-11-11T23:59:00.123456788Z'},precise),true)
})

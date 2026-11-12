import test from 'node:test'
import assert from 'node:assert/strict'
import { createJobNotifications, batchNotification } from '../src/job-notifications.mjs'

const device={id:'pc',name:'书房 PC',wake_method:'wol',probe:{enabled:false}}
const job=(status='running')=>({job_id:'j1',device_id:'pc',action:'wake',status,phase:status==='running'?'wol':'finished',steps:status==='running'?[]:[{name:'wol',status:'success'}]})
test('only tracked local commands announce completion once; history and batch events stay silent',()=>{
  const events=[],notifier=createJobNotifications({notify:toast=>events.push(toast)})
  notifier.handle(job('success'));assert.equal(events.length,0)
  notifier.track(job(),device);notifier.handle({...job(),phase:'probe'});notifier.handle(job('success'));notifier.handle(job('success'))
  assert.equal(events.length,2);assert.equal(events[0].kind,'info');assert.equal(events[1].kind,'success')
  assert.match(events[1].message,/书房 PC.*唤醒包已发送/)
  notifier.handle({...job('failed'),job_id:'batch_job'});assert.equal(events.length,2)
  notifier.clear();notifier.handle(job('success'));assert.equal(events.length,2)
})
test('completion before POST response announces only the result and storage faults never announce success',()=>{
  const events=[],notifier=createJobNotifications({notify:toast=>events.push(toast)})
  notifier.handle(job('success'));notifier.track(job('success'),device)
  assert.equal(events.length,1);assert.equal(events[0].kind,'success')
  notifier.track({...job(),job_id:'fault'},device)
  notifier.handle({...job(),job_id:'fault',code:'storage_unavailable'})
  notifier.handle({...job(),job_id:'fault',code:'storage_unavailable'})
  assert.equal(events.at(-1).kind,'error');assert.equal(events.length,3)
})
test('batch notifications report one aggregate and never claim running tasks are complete',()=>{
  assert.equal(batchNotification([{status:'running'}]),null)
  const success=batchNotification([{status:'success'},{status:'success'}])
  assert.equal(success.kind,'success');assert.match(success.message,/成功 2 台/)
  const partial=batchNotification([{status:'success'},{status:'partial'},{status:'timeout'}])
  assert.equal(partial.kind,'warning');assert.match(partial.message,/成功 1 台，部分完成 1 台，未完成 1 台/)
  assert.equal(batchNotification([{status:'failed'}]).kind,'error')
  assert.equal(batchNotification([{status:'pending'}],true).kind,'warning')
})

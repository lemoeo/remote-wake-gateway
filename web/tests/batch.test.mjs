import test from 'node:test'
import assert from 'node:assert/strict'
import { runBatch } from '../src/batch.mjs'

const rows = () => [
  {device_id:'pc', name:'PC', command_id:'cmd_pc', status:'pending'},
  {device_id:'mac', name:'Mac', command_id:'cmd_mac', status:'pending'},
]
test('batch waits for each WakeJob and keeps partial results', async () => {
  const data=rows(), calls=[]
  await runBatch(data, {
    submit:async id=>{calls.push('submit:'+id);return {device_id:id,job_id:'job_'+id,status:'running'}},
    waitForJob:async job=>{calls.push('event:'+job.job_id);return {...job,status:job.device_id==='pc'?'success':'partial'}},
  })
  assert.deepEqual(calls,['submit:pc','event:job_pc','submit:mac','event:job_mac'])
  assert.deepEqual(data.map(r=>r.status),['success','partial'])
})
test('uncertain submit pauses and explicit continuation reuses the same command key', async () => {
  const data=rows(), keys=[];let offline=true
  const adapters={submit:async(id,key)=>{keys.push(key);if(offline)throw new TypeError('connection lost');return {device_id:id,job_id:'job_'+id,status:'success'}},getJob:async()=>{throw Error('unexpected poll')}}
  assert.equal((await runBatch(data,adapters)).paused,true)
  assert.equal(data[0].status,'paused');assert.equal(data[1].status,'pending')
  offline=false
  await runBatch(data,adapters)
  assert.deepEqual(keys,['cmd_pc','cmd_pc','cmd_mac'])
})
test('explicit stop lets current task finish and leaves remaining devices unsubmitted', async () => {
  const data=rows();let stopped=false;let submits=0
  await runBatch(data,{submit:async id=>{submits++;stopped=true;return {device_id:id,job_id:'job_'+id,status:'running'}},waitForJob:async job=>({...job,status:'success'}),shouldStop:()=>stopped})
  assert.equal(submits,1);assert.equal(data[0].status,'success');assert.equal(data[1].status,'pending')
})
test('same-device busy response reuses the job but another device target lock is preserved', async () => {
  for(const owner of ['pc','other']) {
    const data=[rows()[0]]
    await runBatch(data,{submit:async()=>{throw Object.assign(Error('busy'),{status:409,code:'device_busy',jobID:'job_existing'})},getJob:async()=>({device_id:owner,job_id:'job_existing',status:'success'})})
    assert.equal(data[0].status,owner==='pc'?'success':'failed')
  }
})
test('authentication/rate limit stops the batch without retrying other devices', async () => {
  for(const status of [401,403,429,503]) {
    const data=rows();let submits=0
    await runBatch(data,{submit:async()=>{submits++;throw Object.assign(Error('limited'),{status})},getJob:async()=>{}})
    assert.equal(submits,1);assert.equal(data[0].status,'paused');assert.equal(data[1].status,'pending')
  }
})

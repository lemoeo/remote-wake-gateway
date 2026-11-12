import test from 'node:test'
import assert from 'node:assert/strict'
import { createRealtime } from '../src/realtime.mjs'
import { runBatch, createBatchGuard } from '../src/batch.mjs'
import { createDeviceSync } from '../src/device-sync.mjs'

const job = (status='running', id='j1') => ({job_id:id,device_id:'pc',status,created_at:'2026-01-01T00:00:00Z'})
const deferred = () => { let resolve, reject; const promise = new Promise((a,b)=>{resolve=a;reject=b}); return {promise,resolve,reject} }
class Source {
  listeners = new Map(); readyState = 1; closed = false
  addEventListener(type, cb) { this.listeners.set(type, cb) }
  emit(type, value) { return this.listeners.get(type)?.({data:JSON.stringify(value)}) }
  close() { this.closed = true }
}
async function fixture(t, overrides={}) {
  const calls=[], sources=[], updates=[], statuses=[]
  const live=createRealtime({request:async path=>{calls.push(path);return {authenticated:true}},createSource:path=>{calls.push(path);const source=new Source();sources.push(source);return source},onJob:j=>updates.push(j),onStatus:s=>statuses.push(s),...overrides})
  t.after(()=>live.stop())
  live.start();await sources[0].emit('resync',{ready:true})
  return {live,calls,sources,updates,statuses}
}
test('idle stream and reconnection never read history or poll task details', async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  const {live,calls,sources}=await fixture(t)
  assert.equal(live.connected(),true)
  t.mock.timers.tick(60000)
  assert.deepEqual(calls,['/api/v1/events'])
  sources[0].onerror();await sources[0].emit('resync',{ready:true})
  assert.equal(live.connected(),true)
  assert.deepEqual(calls,['/api/v1/events','/auth/session'])
})
test('batch advances only on terminal events and disconnect pauses further submissions', async t=>{
  const {live,sources}=await fixture(t)
  const rows=[{device_id:'pc',command_id:'key1',status:'pending'},{device_id:'pc2',command_id:'key2',status:'pending'}]
  let submissions=0
  const first=deferred()
  const running=runBatch(rows,{submit:async()=>{submissions++;first.resolve();return live.remember(job())},getJob:live.readJob,waitForJob:live.waitForJob})
  await first.promise;await Promise.resolve()
  assert.equal(submissions,1)
  sources[0].emit('job.updated',{job:job('running')})
  assert.equal(submissions,1)
  sources[0].onerror()
  assert.equal((await running).paused,true)
  assert.equal(rows[0].job_id,'j1');assert.equal(rows[0].command_id,'key1')
  assert.equal(rows[1].status,'pending');assert.equal(submissions,1)
  sources[0].emit('job.updated',{job:job('success')})
  assert.equal(live.values()[0].status,'success')
})
test('concurrent detail reads are deduplicated and an event wins over stale HTTP', async t=>{
  const response=deferred();let count=0
  const {live,sources}=await fixture(t,{request:async()=>{count++;return response.promise}})
  const mark=live.checkpoint(), first=live.readJob('j1'), second=live.readJob('j1')
  assert.equal(first,second)
  sources[0].emit('job.updated',{job:job('success')})
  response.resolve(job())
  assert.equal((await first).status,'success');assert.equal(count,1)
  assert.equal(live.newer(job(),mark).status,'success')
  const d=live.overlayDevices([{id:'pc',active_job_id:'j1'}],mark)
  assert.equal(d[0].active_job_id,'')
})
test('completion arriving before POST response cannot be overwritten', async t=>{
  const response=deferred()
  const {live,sources}=await fixture(t,{request:()=>response.promise})
  const submitted=live.submit('pc','key')
  sources[0].emit('job.updated',{job:job('success')})
  response.resolve(job('pending'))
  assert.equal((await submitted).status,'success')
  assert.equal((await live.waitForJob(job('pending'))).status,'success')
})
test('reconnect reads only known and device-active IDs, with no recent-job list', async t=>{
  const calls=[]
  const {live,sources}=await fixture(t,{getActiveIDs:()=>['j2','j1'],request:async path=>{calls.push(path);return path==='/auth/session'?{authenticated:true}:job('success',path.split('/').pop())}})
  live.remember(job());calls.length=0
  sources[0].onerror();await sources[0].emit('resync',{ready:true})
  assert.equal(live.connected(),true)
  assert.deepEqual(calls.sort(),['/auth/session','/wake-jobs/j1','/wake-jobs/j2'])
  assert.equal(live.values().every(j=>j.status==='success'),true)
})
test('event completion resolves waiters and storage failure blocks orchestration', async t=>{
  const {live,sources}=await fixture(t)
  live.remember(job())
  const completion=live.waitForJob(job())
  sources[0].emit('job.updated',{job:job('success')})
  assert.equal((await completion).status,'success')
  live.remember(job('running','j2'))
  const blocked=assert.rejects(live.waitForJob(job('running','j2')),/disk/)
  sources[0].emit('storage.error',{message:'disk failed'})
  await blocked;assert.equal(live.connected(),false)
})
test('session shutdown discards in-flight submission and read snapshots', async t=>{
  const response=deferred()
  const {live}=await fixture(t,{request:()=>response.promise})
  const submitting=live.submit('pc','key'), reading=live.readJob('j1')
  const rejected=assert.rejects(reading,/实时连接/)
  live.stop();live.clear();response.resolve(job())
  await submitting;await rejected
  assert.deepEqual(live.values(),[])
})
test('terminal HTTP stream errors reopen with delay; authentication expiry stops stream', async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  let expired=0
  const {live,sources}=await fixture(t,{onAuthExpired:()=>expired++})
  sources[0].readyState=2;sources[0].onerror()
  assert.equal(sources.length,1)
  t.mock.timers.tick(3000)
  assert.equal(sources.length,2)
  await sources[1].emit('resync',{ready:true})
  sources[1].emit('auth.expired',{})
  assert.equal(expired,1);assert.equal(live.connected(),false);assert.equal(sources[1].closed,true)
})
test('completed cache is bounded while active jobs remain available', async t=>{
  const {live}=await fixture(t)
  live.remember(job())
  for(let i=0;i<300;i++)live.remember(job('success','done'+i))
  assert.equal(live.values().length,256)
  assert.equal(live.values().some(j=>j.job_id==='j1'),true)
})
test('initial ready waits for the subscribed device snapshot, with one-shot fallback', async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  const devices=deferred(), sources=[]
  const live=createRealtime({request:async()=>({authenticated:true}),createSource:()=>{const s=new Source();sources.push(s);return s},onDevices:()=>devices.promise})
  t.after(()=>live.stop())
  const ready=live.start()
  const sync=sources[0].emit('resync',{ready:true})
  let settled=false;ready.then(()=>settled=true)
  await Promise.resolve();assert.equal(settled,false)
  devices.resolve();await sync
  assert.equal(await ready,true)
  const fallback=live.start()
  t.mock.timers.tick(2500)
  assert.equal(await fallback,false)
  assert.equal(sources.length,2)
})
test('waiting for a job times out once without polling or retrying the command', async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  const {live,calls}=await fixture(t)
  live.remember(job())
  const waiting=assert.rejects(live.waitForJob(job()),/等待任务结果超时/)
  t.mock.timers.tick(180000)
  await waiting
  assert.deepEqual(calls,['/api/v1/events'])
  assert.equal(live.values()[0].status,'running')
})
test('cleanup events evict matching snapshots without fetching history or losing active tasks',async t=>{
  const cleared=[]
  const {live,sources,calls}=await fixture(t,{onHistoryCleared:(plan,ids)=>cleared.push({plan,ids})})
  live.remember({...job('success'),finished_at:'2026-01-01T00:00:00Z',expires_at:'2026-01-01T00:00:00Z'})
  live.remember(job('running','active'))
  const plan={scope:'completed',before:'2026-11-12T00:00:00Z',retention_days:7}
  sources[0].emit('history.cleared',{cleanup:plan})
  assert.deepEqual(live.values().map(j=>j.job_id),['active'])
  assert.deepEqual(cleared,[{plan,ids:['j1']}])
  assert.deepEqual(calls,['/api/v1/events'])
})
test('an HTTP snapshot already in flight cannot repopulate a cleared cache',async t=>{
  const response=deferred()
  const {live,sources}=await fixture(t,{request:()=>response.promise})
  const reading=live.readJob('j1'), rejected=assert.rejects(reading,/记录已发生变化/)
  sources[0].emit('history.invalidated',{})
  response.resolve(job('success'))
  await rejected;assert.deepEqual(live.values(),[])
})
test('reconnecting a task removed by cleanup does not enter a reconnect loop',async t=>{
  const missing=[]
  const {live,sources}=await fixture(t,{onMissing:id=>missing.push(id),request:async path=>{
    if(path==='/auth/session')return {authenticated:true}
    throw Object.assign(new Error('not found'),{status:404})
  }})
  live.remember(job())
  sources[0].onerror();await sources[0].emit('resync',{ready:true})
  assert.equal(live.connected(),true);assert.deepEqual(missing,['j1']);assert.deepEqual(live.values(),[])
})
test('disconnect before POST response stays paused after reconnect until explicit continuation',async t=>{
  const response=deferred(),submitted=[]
  const done=id=>({job_id:'job_'+id,device_id:id,status:'success'})
  const {live,sources}=await fixture(t,{request:async path=>{
    if(path==='/auth/session')return {authenticated:true}
    if(path==='/devices/pc/wake'){submitted.push('pc');return response.promise}
    if(path==='/devices/mac/wake'){submitted.push('mac');return done('mac')}
    if(path==='/wake-jobs/job_pc')return done('pc')
    throw Error(path)
  }})
  const rows=['pc','mac'].map(id=>({device_id:id,command_id:'cmd_'+id,status:'pending'}))
  const adapters=guard=>({submit:live.submit,getJob:live.readJob,waitForJob:(j,update)=>{guard.assertCurrent();return live.waitForJob(j,update)},shouldStop:()=>guard.interrupted()||!live.connected()})
  const guard=createBatchGuard({generation:()=>live.generation,session:()=>1})
  const running=runBatch(rows,adapters(guard))
  await Promise.resolve();sources[0].onerror();await sources[0].emit('resync',{ready:true})
  response.resolve({...done('pc'),status:'pending'})
  assert.equal((await running).paused,true)
  assert.deepEqual(submitted,['pc']);assert.equal(rows[1].status,'pending')
  await runBatch(rows,adapters(createBatchGuard({generation:()=>live.generation,session:()=>1})))
  assert.deepEqual(submitted,['pc','mac'])
})
test('a new login cannot continue a previous session batch',async t=>{
  let session=1
  const response=deferred(),submits=[]
  const {live,sources}=await fixture(t,{request:async path=>{submits.push(path);return response.promise}})
  const guard=createBatchGuard({generation:()=>live.generation,session:()=>session})
  const rows=['pc','mac'].map(id=>({device_id:id,command_id:'cmd_'+id,status:'pending'}))
  const running=runBatch(rows,{submit:live.submit,getJob:live.readJob,waitForJob:(j,update)=>{guard.assertCurrent();return live.waitForJob(j,update)},shouldStop:guard.interrupted})
  await Promise.resolve();live.stop();session++;live.start();await sources[1].emit('resync',{ready:true})
  response.resolve({job_id:'old_job',device_id:'pc',status:'running'})
  assert.equal((await running).paused,true)
  assert.deepEqual(submits,['/devices/pc/wake']);assert.equal(rows[0].job_id,'old_job')
})
test('failed device invalidation marks synchronization stale and refresh recovers without polling',async t=>{
  t.mock.timers.enable({apis:['setTimeout']})
  let fail=true
  const loads=[]
  const sync=createDeviceSync({load:async revision=>{loads.push(revision);if(fail)throw Error('snapshot failed')}})
  const {live,sources,statuses,calls}=await fixture(t,{onDevices:revision=>revision?sync.schedule(revision):Promise.resolve()})
  const guard=createBatchGuard({generation:()=>live.generation,session:()=>1})
  sources[0].emit('devices.changed',{revision:2});sources[0].emit('devices.changed',{revision:3})
  t.mock.timers.tick(60);await new Promise(resolve=>setImmediate(resolve))
  assert.equal(statuses.at(-1),'stale');assert.equal(live.connected(),false)
  fail=false;await live.refresh()
  assert.equal(live.connected(),true);assert.equal(guard.interrupted(),true)
  assert.deepEqual(loads,[3]);assert.deepEqual(calls,['/api/v1/events'])
})
test('manual refresh reconciles known jobs without EventSource support or a recent-job list',async t=>{
  const calls=[];let devices=0
  const live=createRealtime({createSource:()=>{throw Error('unsupported')},onDevices:async()=>devices++,request:async path=>{calls.push(path);return {...job('success')}}})
  t.after(()=>live.stop());await live.start();live.remember(job())
  await live.refresh()
  assert.equal(devices,1);assert.deepEqual(calls,['/wake-jobs/j1'])
  assert.equal(live.values()[0].status,'success');assert.equal(live.connected(),false)
})
test('bounded automatic cleanup evicts only announced IDs without a history fetch',async t=>{
  const missing=[]
  const {live,sources,calls}=await fixture(t,{onMissing:id=>missing.push(id)})
  live.remember(job('success','expired'));live.remember(job('success','retained'));live.remember(job('running','active'))
  sources[0].emit('history.removed',{job_ids:['expired']})
  assert.deepEqual(missing,['expired'])
  assert.deepEqual(live.values().map(j=>j.job_id).sort(),['active','retained'])
  assert.deepEqual(calls,['/api/v1/events'])
})

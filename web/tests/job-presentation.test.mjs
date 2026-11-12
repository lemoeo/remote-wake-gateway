import test from 'node:test'
import assert from 'node:assert/strict'
import { presentJob, newestForCard } from '../src/job-presentation.mjs'

const pc={id:'pc',name:'PC',wake_method:'wol',probe:{enabled:false}}
const mac={...pc,id:'mac',name:'Mac',wake_method:'mac_vnc'}
const job=(changes={})=>({job_id:'j1',command_id:'cmd1',device_id:'pc',action:'wake',status:'running',phase:'starting',created_at:'2026-11-12T00:00:00Z',finished_at:'0001-01-01T00:00:00Z',steps:[],observation:{state:'unknown'},...changes})
test('Mac phases expose connection, identity, authentication and hold progress without early completion',()=>{
  for(const [phase,title,step] of [['connecting','正在连接 Mac','connect'],['authenticating','正在验证 Mac 身份','auth'],['authenticated','VNC 认证成功','auth'],['holding','VNC 认证成功，正在保持连接','auth']]){
    const view=presentJob(job({phase,steps:[{name:'vnc',status:'running'}]}),mac)
    assert.equal(view.title,title);assert.equal(view.active,true)
    assert.equal(view.steps.find(s=>s.name==='finish').state,'pending')
    assert.equal(view.steps.find(s=>s.name===step).state,['authenticated','holding'].includes(phase)?'done':'active')
    assert.equal(view.time,'2026-11-12T00:00:00Z')
  }
})
test('WoL completion says packet sent and optional probe describes reachability',()=>{
  const sent=job({status:'success',phase:'finished',steps:[{name:'wol',status:'success'}]})
  const view=presentJob(sent,pc)
  assert.equal(view.title,'唤醒包已发送');assert.match(view.detail,/不代表设备已开机/)
  assert.equal(view.steps.every(s=>s.state==='done'),true)
  const probing=presentJob(job({phase:'probe',steps:[{name:'wol',status:'success'},{name:'probe',status:'running'}]}),{...pc,probe:{enabled:true}})
  assert.equal(probing.title,'正在等待目标端口响应')
  assert.equal(probing.steps.find(s=>s.name==='wol').state,'done')
  assert.equal(probing.steps.find(s=>s.name==='probe').state,'active')
  const reached=presentJob({...sent,steps:[...sent.steps,{name:'probe',status:'success'}]},pc)
  assert.match(reached.detail,/目标端口可达/)
})
test('combined wake keeps failed WoL visible while VNC is still connecting',()=>{
  const combined={...mac,wake_method:'wol_mac_vnc'}
  const progress=job({phase:'connecting',steps:[{name:'wol',status:'failed',message:'发送失败'},{name:'vnc',status:'running'}]})
  const view=presentJob(progress,combined)
  assert.equal(view.title,'正在连接 Mac');assert.equal(view.steps[0].state,'failed')
  const partial=presentJob({...progress,status:'partial',phase:'finished',steps:[progress.steps[0],{name:'vnc',status:'success'}]},combined)
  assert.equal(partial.tone,'warning');assert.match(partial.detail,/VNC 认证完成/)
  assert.equal(partial.steps.find(s=>s.name==='finish').state,'uncertain')
})
test('failures retain concrete reasons and distinguish timeout, cancellation and uncertain authentication',()=>{
  const failed=presentJob(job({status:'failed',phase:'finished',steps:[{name:'vnc',status:'failed',code:'auth_rejected',message:'Mac 拒绝了凭据，请检查账户'}]}),mac)
  assert.equal(failed.tone,'error');assert.match(failed.detail,/拒绝了凭据/)
  const timeout=presentJob(job({status:'timeout',steps:[{name:'wol',status:'success'},{name:'probe',status:'failed',code:'timeout'}]}),pc)
  assert.match(timeout.detail,/唤醒包已发送.*检测超时/)
  const unknown=presentJob(job({status:'failed',steps:[{name:'vnc',status:'failed',code:'auth_result_unknown',message:'认证结果不明'}]}),mac)
  assert.equal(unknown.tone,'warning');assert.equal(unknown.steps.find(s=>s.name==='auth').state,'uncertain')
  const cancelled=presentJob(job({status:'cancelled'}),pc)
  assert.equal(cancelled.title,'任务已取消');assert.match(cancelled.detail,/无法撤销/)
  const storage=presentJob(job({code:'storage_unavailable'}),pc)
  assert.equal(storage.tone,'error');assert.equal(storage.title,'任务结果无法保存')
})
test('new submissions replace old results, but authoritative matching jobs beat uncertain HTTP errors',()=>{
  const old=job({status:'success',steps:[{name:'wol',status:'success'}]})
  assert.equal(presentJob(old,pc,{state:'submitting',action:'wake',command_id:'cmd2'}).title,'正在提交唤醒任务')
  const uncertain={state:'error',action:'wake',command_id:'cmd2',uncertain:true,message:'连接中断'}
  assert.equal(presentJob(old,pc,uncertain).tone,'warning')
  assert.equal(presentJob({...old,command_id:'cmd2'},pc,uncertain).title,'唤醒包已发送')
  assert.equal(presentJob(undefined,{...pc,active_job_id:'other'}).jobID,'other')
})
test('viewing an older history entry does not replace the card current operation, including nanosecond timestamps',()=>{
  const current=job({job_id:'current',created_at:'2026-11-12T00:00:00.123456789Z'})
  assert.equal(newestForCard(job({created_at:'2026-11-12T00:00:00.123456788Z'}),current),false)
  assert.equal(newestForCard({...current,phase:'authenticating'},current),true)
  assert.equal(newestForCard(job({created_at:'2026-11-12T00:00:00.123456790Z'}),current),true)
})

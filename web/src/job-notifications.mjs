import { jobActive, presentJob } from './job-presentation.mjs'

// Track only commands initiated in this page. Reconciliation and history reads
// must never replay completion notifications, and batch jobs stay silent.
export function createJobNotifications({notify}) {
  const tracked=new Map()
  function handle(job) {
    const record=tracked.get(job.job_id)
    if(!record)return
    if(job.code==='storage_unavailable') {
      if(!record.storageNotified){record.storageNotified=true;notify({key:job.job_id,kind:'error',message:`${record.device.name}：任务结果无法保存，请检查 Gateway 存储`})}
      return
    }
    if(jobActive(job)||record.finished)return
    record.finished=true
    const view=presentJob(job,record.device)
    notify({key:job.job_id,kind:view.tone==='idle'?'info':view.tone,message:`${record.device.name}：${view.title}`+(view.tone==='error'||view.tone==='warning'?`。${view.detail}`:'')})
  }
  function track(job,device,{existing=false}={}) {
    if(!tracked.has(job.job_id)) {
      tracked.set(job.job_id,{device:{...device},finished:false,storageNotified:false})
      for(const [id,record] of tracked){if(tracked.size<=256)break;if(record.finished)tracked.delete(id)}
      if(jobActive(job))notify({key:job.job_id,kind:'info',message:existing?`${device.name}：已有任务正在执行`:`已提交 ${device.name} 的${job.action==='probe'?'检测':'唤醒'}任务`})
    }
    handle(job)
  }
  return {track,handle,forget:id=>tracked.delete(id),clear:()=>tracked.clear()}
}

export function batchNotification(rows,paused=false) {
  if(!rows.length)return null
  if(paused)return {kind:'warning',message:'批次已暂停，已创建的任务继续执行；可点击“继续未完成”确认结果'}
  if(rows.some(r=>!['success','partial','failed','timeout','cancelled','expired','interrupted'].includes(r.status)))return null
  const success=rows.filter(r=>r.status==='success').length,partial=rows.filter(r=>r.status==='partial').length
  const unfinished=rows.length-success-partial
  return {kind:unfinished||partial?success||partial?'warning':'error':'success',message:`批量处理完成：成功 ${success} 台`+(partial?`，部分完成 ${partial} 台`:'')+(unfinished?`，未完成 ${unfinished} 台`:'')}
}

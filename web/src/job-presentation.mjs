import { timestamp } from './history.mjs'

export const jobActive = job => job && ['pending', 'running'].includes(job.status)
export function observationLabel(observation, now) {
  const observed = Date.parse(observation?.observed_at)
  if (!Number.isFinite(observed) || observed <= 0 || now - observed >= 60000) return '状态未知'
  return observation.state === 'service_ready' ? 'VNC 已验证' : observation.state === 'reachable' ? '端口可达' : '状态未知'
}
export function newestForCard(job, previous) {
  if(!previous || job.job_id===previous.job_id)return true
  const incoming=timestamp(job.created_at), old=timestamp(previous.created_at)
  return incoming!==undefined && (old===undefined || incoming>=old)
}
const phases = {
  queued: '排队等待执行', starting: '正在准备唤醒', wol: '正在发送唤醒包',
  vnc: '正在准备屏幕共享', connecting: '正在连接 Mac', authenticating: '正在验证 Mac 身份',
  authenticated: 'VNC 认证成功', holding: 'VNC 认证成功，正在保持连接', probe: '正在等待目标端口响应',
}

export function presentJob(job, device, submission) {
  const matching = job && job.command_id === submission?.command_id
  if (submission && !matching && !jobActive(job)) {
    if (submission.state === 'submitting') return { active:true, tone:'running', title:submission.action==='probe'?'正在提交检测任务':'正在提交唤醒任务', detail:'正在等待 Gateway 确认', steps:[] }
    return { active:false, tone:submission.uncertain?'warning':'error', title:submission.state==='paused'?'批次已暂停':submission.uncertain?'提交结果待确认':submission.action==='probe'?'检测未能提交':'唤醒未能提交', detail:submission.message, steps:[] }
  }
  if (!job) return device.active_job_id
    ? { active:true, tone:'running', title:'任务正在执行', detail:'正在同步任务进度', steps:[], jobID:device.active_job_id }
    : { active:false, tone:'idle', title:'等待操作', detail:'执行过程和结果将在这里显示', steps:[] }

  const running = jobActive(job), steps = job.steps || []
  const wol = steps.find(s=>s.name==='wol'), vnc = steps.find(s=>s.name==='vnc'), probe = steps.find(s=>s.name==='probe')
  const hasWol = job.action!=='probe' && (Boolean(wol) || running && device.wake_method!=='mac_vnc')
  const hasVNC = job.action!=='probe' && (Boolean(vnc) || running && device.wake_method!=='wol')
  const hasProbe = job.action==='probe' || Boolean(probe) || running && !hasVNC && device.probe?.enabled
  const authenticated = vnc?.status==='success' || job.observation?.state==='service_ready' || ['authenticated','holding'].includes(job.phase)
  const connected = authenticated || job.observation?.state==='reachable' && hasVNC || ['authenticating','authenticated','holding'].includes(job.phase) || /^auth_/.test(vnc?.code || '')
  const pending = running ? 'pending' : 'skipped'
  const stepState = step => step?.status==='success'?'done':step?.status==='failed'?'failed':step?.status==='running'&&running?'active':pending
  const markers = []
  if (hasWol) markers.push({name:'wol',label:'发送',state:stepState(wol)})
  if (hasVNC) {
    markers.push({name:'connect',label:'连接',state:connected?'done':running&&['vnc','connecting'].includes(job.phase)?'active':vnc?.status==='failed'?'failed':pending})
    markers.push({name:'auth',label:'认证',state:authenticated?'done':vnc?.code==='auth_result_unknown'?'uncertain':running&&job.phase==='authenticating'?'active':vnc?.status==='failed'&&connected?'failed':pending})
  }
  if (hasProbe) markers.push({name:'probe',label:'检测',state:stepState(probe)})
  markers.push({name:'finish',label:'完成',state:running?'pending':job.status==='success'?'done':['partial','interrupted','expired'].includes(job.status)?'uncertain':job.status==='cancelled'?'skipped':'failed'})

  let tone='running', title=phases[job.phase] || '任务正在执行', detail='任务在后台执行，可随时查看详情'
  const failed = steps.find(s=>s.status==='failed')
  if (running) {
    if (job.phase==='starting') title=job.action==='probe'?'正在准备端口检测':hasWol?'正在准备唤醒包':'正在准备屏幕共享'
    if (job.phase==='wol' && wol?.status==='success') { title='唤醒包已发送'; detail=hasVNC?'正在准备连接 Mac':hasProbe?'正在准备上线检测':'正在保存任务结果' }
    if (job.phase==='probe' && job.action==='probe') title='正在检测目标端口'
    if (job.phase==='holding') detail='认证已通过，正在完成配置的保持步骤'
    if (job.status==='pending') { title='排队等待执行';detail='任务已提交，等待 Gateway 执行' }
  } else if (job.status==='success') {
    tone='success'
    if (job.action==='probe') { title='目标端口可达';detail='TCP 检测已完成' }
    else if (hasVNC) { title=hasWol?'唤醒流程已完成':'VNC 认证成功';detail=hasWol?'唤醒包已发送，屏幕共享认证完成':'唤醒流程已完成' }
    else { title='唤醒包已发送';detail=probe?.status==='success'?'上线检测通过，目标端口可达':'已发送 Magic Packet，不代表设备已开机' }
  } else if (job.status==='partial') {
    tone='warning';title='部分步骤已完成'
    detail=wol?.status==='success'?'唤醒包已发送；'+(failed?.message || '后续步骤未完成'):vnc?.status==='success'?'VNC 认证完成；'+(failed?.message || '唤醒包发送失败'):failed?.message || job.message
  } else if (job.status==='timeout') {
    tone='error';title='执行超时'
    detail=wol?.status==='success' && probe?'唤醒包已发送，但目标端口检测超时':failed?.message || job.message || '等待目标响应超时'
  } else if (job.status==='cancelled') {
    tone='idle';title='任务已取消';detail='已发送的请求无法撤销'
  } else if (['interrupted','expired'].includes(job.status)) {
    tone='warning';title=job.status==='expired'?'任务已过期':'任务结果不明';detail=job.message || '请先确认设备状态，再决定下一步操作'
  } else {
    tone='error';title=job.action==='probe'?'端口检测失败':'唤醒失败';detail=failed?.message || job.message || '操作未完成，请查看详情'
    if (vnc?.code==='auth_result_unknown') {tone='warning';title='认证结果待确认'}
  }
  if (job.code==='storage_unavailable') {tone='error';title='任务结果无法保存';detail=job.message || '请检查 Gateway 存储并重启服务'}
  const finishedTime=job.finished_at&&!job.finished_at.startsWith('0001-')?job.finished_at:job.created_at
  return {active:running,tone,title,detail,steps:markers,jobID:job.job_id,time:running?job.created_at:finishedTime,finished:!running}
}

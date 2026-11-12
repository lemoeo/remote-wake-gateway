<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { request, setCSRF, commandKey } from './api.mjs'
import { interfaceOptions, suggestedBroadcasts, broadcastOnSelection, broadcastWarnings } from './wol-form.mjs'
import type { Device, Job, Settings, BatchRow, DeviceDefaults, HistoryCleanup, ToastItem, SubmissionState } from './types'
import { runBatch, batchFinished, createBatchGuard } from './batch.mjs'
import { useBatchPanel } from './batch-panel.mjs'
import { createRealtime } from './realtime.mjs'
import { matchesHistoryCleanup } from './history.mjs'
import { createDeviceEditor } from './device-editor.mjs'
import { createDeviceSync } from './device-sync.mjs'
import { createToasts } from './toasts.mjs'
import { createJobNotifications, batchNotification } from './job-notifications.mjs'
import { newestForCard } from './job-presentation.mjs'
import ToastNotice from './ToastNotice.vue'
import DeviceActivity from './DeviceActivity.vue'

const loading = ref(true), authenticated = ref(false), initialized = ref(true), busy = ref(false)
const password = ref(''), setupToken = ref(''), error = ref(''), notice = ref('')
const page = ref('devices'), search = ref(''), devices = ref<Device[]>([]), jobs = ref<Job[]>([]), revision = ref(1)
const recentJobs = ref<Job[]>([]), realtimeStatus = ref('connecting'), historyLoading = ref(false), historyDirty = ref(false), clock = ref(Date.now())
let stopped = false, sessionEpoch = 0, historyRequest = 0, historyLoaded = false, historyVersion = 0
let deviceLoad: Promise<void> | undefined, deviceDirty = false, clockTimer: ReturnType<typeof setTimeout> | undefined
const gateway = ref({name: 'RemoteWake Gateway', version: '', gateway_id: '', uptime_seconds: 0})
const settings = ref<Settings | null>(null), networks = ref('')
const cleanupScope = ref('expired'), cleanupPreview = ref<HistoryCleanup | null>(null)
const interfaces = ref<{name: string; ip: string; broadcast: string}[]>([])
const editing = ref<Device | null>(null), isNew = ref(false), vncPassword = ref(''), clearPassword = ref(false)
const sendInterfaces = computed(() => interfaceOptions(interfaces.value))
const broadcastSuggestions = computed(() => editing.value ? suggestedBroadcasts(interfaces.value, editing.value.wol.interface) : [])
const broadcastHints = computed(() => editing.value ? broadcastWarnings(interfaces.value, editing.value.wol.interface, editing.value.wol.broadcast) : [])
const selected = ref<Job | null>(null), importText = ref(''), importPreview = ref(false), historyCursor = ref('')
const modalOpen = computed(() => authenticated.value && Boolean(editing.value || selected.value))
watch(modalOpen, open => document.body.classList.toggle('modal-open', open))
const pendingKeys = new Map<string, string>()
const optionalOpen = ref(false)
const deviceEditor = createDeviceEditor({request,commandKey,confirmDelete:(message:string)=>confirm(message)})
const deviceSync = createDeviceSync({load:loadDevices})
const savedDeviceDefaults = ref<DeviceDefaults>({broadcast:'255.255.255.255',port:9,repeat:3})
const cardJobs = ref<Record<string,Job>>({}), submissions = ref<Record<string,SubmissionState>>({})
const toast = ref<ToastItem|null>(null)
const toastQueue = createToasts({onChange:(item:ToastItem|null)=>{toast.value=item}})
const jobNotifications = createJobNotifications({notify:toastQueue.show})
let batchCompletionNotified=false, batchNoticeKey=''
watch(notice,text=>{if(text&&authenticated.value)toastQueue.show({key:'notice',kind:'success',message:text})},{flush:'sync'})
watch(error,text=>{if(text&&authenticated.value&&!editing.value)toastQueue.show({key:'error',kind:'error',message:text})},{flush:'sync'})
const chosen = ref<string[]>([]), batchRows = ref<BatchRow[]>([]), batchRunning = ref(false), batchStop = ref(false), batchMessage = ref(''), batchConfirming = ref(false)
const manualAfterBatch = ref(new Set<string>())
const canWake = (d: Device) => d.enabled && !d.active_job_id && (d.wake_method==='wol' || Boolean(d.password_configured))
const selectable = computed(() => filtered.value.filter(canWake).slice(0,50))
const allChosen = computed(() => selectable.value.length>0 && selectable.value.every(d=>chosen.value.includes(d.id)))
const someChosen = computed(() => selectable.value.some(d=>chosen.value.includes(d.id)) && !allChosen.value)
const batchPending = computed(() => batchRows.value.some(r=>!batchFinished(r.status)))
const { visible: batchVisible, collapsed: batchCollapsed, summary: batchSummary, reset: resetBatchPanel, toggle: toggleBatchPanel, close: closeBatchPanel } = useBatchPanel(batchRows, batchRunning)
const active = (j: Job) => ['pending', 'running'].includes(j.status)
const realtime = createRealtime({
  request,
  onJob: applyJob,
  onHistoryCleared: historyCleared,
  onMissing: missingJob,
  onDevices: (target: number) => target ? deviceSync.schedule(target) : loadDevices(),
  onStatus: (state: string) => {
    realtimeStatus.value = state
    if(state==='disconnected'&&historyLoaded)historyDirty.value=true
  },
  onAuthExpired: () => { endSession(); error.value='会话已过期，请重新登录' },
  getActiveIDs: () => [
    ...devices.value.map(d=>d.active_job_id),
    ...jobs.value.filter(active).map(j=>j.job_id),
    ...batchRows.value.filter(r=>!batchFinished(r.status)).map(r=>r.job_id),
    selected.value&&active(selected.value)?selected.value.job_id:undefined,
  ].filter((id): id is string => Boolean(id)),
})
const filtered = computed(() => devices.value.filter(d => `${d.name} ${d.ip}`.toLowerCase().includes(search.value.toLowerCase())))
const activeCount = computed(() => new Set([...devices.value.map(d=>d.active_job_id), ...recentJobs.value.filter(active).map(j=>j.job_id)].filter(Boolean)).size)
const connectionLabel = computed(() => ({connecting:'正在连接实时更新',syncing:'正在同步状态',connected:'实时更新已连接',disconnected:'实时连接中断，正在重连',unsupported:'实时更新不可用，请手动刷新',blocked:'Gateway 存储异常',stale:'设备同步失败，请刷新',closed:'实时连接已关闭'}[realtimeStatus.value] || '正在连接'))
const methodName = (s: string) => ({wol: 'WoL 魔术包', mac_vnc: 'Mac 屏幕共享', wol_mac_vnc: 'WoL + Mac VNC'}[s] || s)
const statusName = (s: string) => ({pending:'等待执行',running:'正在执行',success:'已完成',partial:'部分完成',failed:'失败',timeout:'超时',cancelled:'已取消',expired:'已过期',interrupted:'结果不明',submitting:'正在提交',paused:'待继续'}[s] || s)
const phaseName = (s: string) => ({queued:'排队中',starting:'准备执行',wol:'发送 Magic Packet',vnc:'准备 VNC',connecting:'等待 Mac 连接',authenticating:'验证 Mac 账户',authenticated:'认证成功',holding:'保持连接',probe:'检测 TCP 端口',finished:'执行结束'}[s] || s)
const date = (s: string) => new Date(s).toLocaleString('zh-CN', {hour12:false})
const latest = (id: string) => cardJobs.value[id]
function activityJob(d:Device){return d.active_job_id?(cardJobs.value[d.id]?.job_id===d.active_job_id?cardJobs.value[d.id]:recentJobs.value.find(j=>j.job_id===d.active_job_id)):cardJobs.value[d.id]}
function submissionFor(d:Device):SubmissionState|undefined{
  if(submissions.value[d.id])return submissions.value[d.id]
  if(manualAfterBatch.value.has(d.id))return
  const row=batchRows.value.find(r=>r.device_id===d.id&&!r.job_id)
  if(!row||!['submitting','paused','failed'].includes(row.status))return
  return {state:row.status==='submitting'?'submitting':row.status==='paused'?'paused':'error',action:'wake',command_id:row.command_id,message:row.message,uncertain:row.status==='paused'}
}
function observation(id: string) { const o = latest(id)?.observation; if(!o?.observed_at || clock.value-Date.parse(o.observed_at)>=60000) return '状态未知'; return o.state==='service_ready' ? 'VNC 已验证' : o.state==='reachable' ? '端口可达' : '状态未知' }
function report(e: unknown) { if((e as {status?:number})?.status===401&&authenticated.value)endSession(); error.value = e instanceof Error ? e.message : '请求失败' }
async function guarded(fn: () => Promise<void>) { if(busy.value)return; busy.value=true;error.value='';notice.value='';try{await fn()}catch(e){report(e)}finally{busy.value=false} }
function endSession(){authenticated.value=false;toastQueue.clear();jobNotifications.clear();cardJobs.value={};submissions.value={};batchCompletionNotified=false;manualAfterBatch.value.clear();sessionEpoch++;historyRequest++;realtime.stop();realtime.clear();setCSRF('');recentJobs.value=[];jobs.value=[];devices.value=[];selected.value=null;editing.value=null;chosen.value=[];batchRows.value=[];batchConfirming.value=false;historyCursor.value='';historyLoaded=false;historyDirty.value=false;cleanupPreview.value=null;historyLoading.value=false;deviceLoad=undefined;deviceDirty=false;page.value='devices';pendingKeys.clear();deviceSync.cancel();if(clockTimer)clearTimeout(clockTimer)}
function updateClock(){
  if(clockTimer)clearTimeout(clockTimer)
  clock.value=Date.now()
  const expiries=Object.values(cardJobs.value).map(j=>j.observation?.observed_at?Date.parse(j.observation.observed_at)+60000:0).filter(t=>t>clock.value)
  if(expiries.length)clockTimer=setTimeout(updateClock,Math.max(1,Math.min(...expiries)-clock.value))
}
function applyJob(job: Job){
  historyVersion++
  if(newestForCard(job,cardJobs.value[job.device_id]))cardJobs.value[job.device_id]=job
  jobNotifications.handle(job)
  recentJobs.value=realtime.values();updateClock()
  const d=devices.value.find(d=>d.id===job.device_id)
  if(d){if(active(job))d.active_job_id=job.job_id;else if(d.active_job_id===job.job_id)d.active_job_id=''}
  if(selected.value?.job_id===job.job_id)selected.value=job
  const index=jobs.value.findIndex(j=>j.job_id===job.job_id)
  if(index>=0)jobs.value[index]=job
  else if(historyLoaded)historyDirty.value=true
  const row=batchRows.value.find(r=>r.job_id===job.job_id)
  if(row){row.status=job.status;row.message=job.message||'任务正在执行'}
}
function missingJob(id: string){
  jobNotifications.forget(id)
  for(const [device,job] of Object.entries(cardJobs.value)){if(job.job_id===id)delete cardJobs.value[device]}
  historyRequest++;historyVersion++;historyLoading.value=false
  jobs.value=jobs.value.filter(j=>j.job_id!==id)
  if(selected.value?.job_id===id)selected.value=null
  recentJobs.value=realtime.values();updateClock()
  historyCursor.value='';if(historyLoaded)historyDirty.value=true
}
function historyCleared(plan: HistoryCleanup | null, ids: string[]){
  for(const [device,job] of Object.entries(cardJobs.value)){if(matchesHistoryCleanup(job,plan)){jobNotifications.forget(job.job_id);delete cardJobs.value[device]}}
  historyRequest++;historyVersion++;historyLoading.value=false
  jobs.value=jobs.value.filter(j=>!matchesHistoryCleanup(j,plan))
  if(selected.value&&matchesHistoryCleanup(selected.value,plan))selected.value=null
  for(const row of batchRows.value){if(row.job_id&&ids.includes(row.job_id)&&batchFinished(row.status))row.job_id=undefined}
  recentJobs.value=realtime.values();updateClock()
  historyCursor.value='';if(historyLoaded)historyDirty.value=true
}
async function previewCleanup(){await guarded(async()=>{
  const session=sessionEpoch
  const plan=await request('/wake-jobs/cleanup?scope='+cleanupScope.value)
  if(session===sessionEpoch)cleanupPreview.value=plan
})}
async function confirmCleanup(){await guarded(async()=>{
  const plan=cleanupPreview.value
  if(!plan)return
  const session=sessionEpoch
  cleanupPreview.value=null
  const result=await request('/wake-jobs/cleanup',{method:'POST',body:JSON.stringify({scope:plan.scope,before:plan.before})})
  if(session!==sessionEpoch)return
  realtime.clearHistory(result)
  notice.value=`已清理 ${result.deleted} 条任务记录；执行中和保护期内的记录已保留。`
})}
async function loadDevices(target=0): Promise<void>{
  if(!authenticated.value||stopped||target&&target<=revision.value)return
  deviceDirty=true
  if(deviceLoad)return deviceLoad
  const session=sessionEpoch
  const pending=(async()=>{
    while(deviceDirty&&authenticated.value&&!stopped&&session===sessionEpoch){
      deviceDirty=false
      const mark=realtime.checkpoint(), data=await request('/devices')
      if(session!==sessionEpoch||!authenticated.value||stopped)return
      if(mark.generation!==realtime.generation){deviceDirty=true;continue}
      devices.value=realtime.overlayDevices(data.devices,mark);revision.value=data.revision
      chosen.value=chosen.value.filter(id=>devices.value.some(d=>d.id===id))
      for(const id of Object.keys(cardJobs.value)){if(!devices.value.some(d=>d.id===id)){delete cardJobs.value[id];delete submissions.value[id]}}
    }
  })().finally(()=>{if(deviceLoad===pending)deviceLoad=undefined})
  deviceLoad=pending
  return pending
}
async function loadJobs(append=false){
  if(page.value!=='history'||!authenticated.value)return
  const current=++historyRequest, session=sessionEpoch, mark=realtime.checkpoint(), version=historyVersion
  historyLoading.value=true
  try{
    const data=await request('/wake-jobs?limit=50'+(append&&historyCursor.value?'&after='+encodeURIComponent(historyCursor.value):''))
    if(current!==historyRequest||session!==sessionEpoch||!authenticated.value)return
    const incoming=data.jobs.map((j:Job)=>realtime.newer(j,mark))
    jobs.value=append?[...jobs.value,...incoming.filter((j:Job)=>!jobs.value.some(old=>old.job_id===j.job_id))]:incoming
    historyCursor.value=data.next_cursor||'';historyLoaded=true;if(!append)historyDirty.value=version!==historyVersion
  }finally{if(current===historyRequest)historyLoading.value=false}
}
async function load(){
  const session=sessionEpoch
  const initialDevices=realtime.start().then(ready=>ready?undefined:loadDevices())
  const [g,iface,cfg]=await Promise.all([request('/gateway'),request('/network/interfaces'),request('/settings'),initialDevices])
  if(session!==sessionEpoch||!authenticated.value||stopped)return
  gateway.value=g;interfaces.value=iface;settings.value=cfg;savedDeviceDefaults.value={...cfg.device_defaults};networks.value=cfg.allowed_networks.join('\n')
}
async function boot(){try{const session=await request('/auth/session');initialized.value=session.initialized;authenticated.value=session.authenticated;setCSRF(session.csrf_token);if(authenticated.value)await load()}catch(e){report(e)}finally{loading.value=false}}
async function login(){await guarded(async()=>{if(!initialized.value){await request('/auth/setup',{method:'POST',body:JSON.stringify({token:setupToken.value,password:password.value})});initialized.value=true;setupToken.value=''}const data=await request('/auth/session',{method:'POST',body:JSON.stringify({password:password.value})});password.value='';page.value='devices';sessionEpoch++;setCSRF(data.csrf_token);authenticated.value=true;await load()})}
async function logout(){await guarded(async()=>{await request('/auth/session',{method:'DELETE'});endSession()})}
async function refreshStatus(){await guarded(async()=>{await realtime.refresh()})}
watch(page,p=>{if(authenticated.value&&p==='history')loadJobs().catch(report)})
function onVisibility(){if(!document.hidden&&authenticated.value&&realtime.connected())realtime.refresh().catch(report)}
async function openJob(id?:string){if(id)selected.value=await realtime.readJob(id)}
function newDevice(){deviceEditor.begin();isNew.value=true;optionalOpen.value=false;vncPassword.value='';clearPassword.value=false;const defaults=savedDeviceDefaults.value;editing.value={id:'',name:'',type:'pc',enabled:true,revision:0,wake_method:'wol',ip:'',note:'',wol:{mac:'',broadcast:defaults.broadcast,interface:'',port:defaults.port,repeat:defaults.repeat,interval_ms:100},vnc:{port:5900,username:'',hold_seconds:0},probe:{enabled:false,port:22,timeout_seconds:60}}}
function edit(d: Device){isNew.value=false;optionalOpen.value=Boolean(d.note||(d.wake_method==='wol'&&(d.ip||d.probe.enabled)));editing.value=JSON.parse(JSON.stringify(d));vncPassword.value='';clearPassword.value=false}
function chooseInterface(){if(!editing.value)return;editing.value.wol.broadcast=broadcastOnSelection(interfaces.value,editing.value.wol.interface,editing.value.wol.broadcast)}
async function saveDevice(){await guarded(async()=>{if(!editing.value)return;const d={...editing.value, id:isNew.value?undefined:editing.value.id, vnc_password:vncPassword.value||undefined, clear_vnc_password:clearPassword.value};await deviceEditor.save(d,isNew.value);editing.value=null;vncPassword.value='';await loadDevices();notice.value='设备已保存'})}
async function remove(d: Device){await guarded(async()=>{const deleted=await deviceEditor.remove(d,(id:string)=>{if(editing.value?.id===id)editing.value=null});if(deleted){await loadDevices();notice.value='设备已删除'}})}
function showSubmission(job: Job, d:Device, action: string, existing=false){
  delete submissions.value[d.id]
  jobNotifications.track(job,d,{existing})
  if(action==='probe')selected.value=job
}
async function submit(d: Device,action='wake'){
  await guarded(async()=>{
    const target=d.id+':'+action
    let key=pendingKeys.get(target)
    if(!key){key=commandKey();pendingKeys.set(target,key)}
    const session=sessionEpoch
    manualAfterBatch.value.add(d.id)
    submissions.value[d.id]={state:'submitting',action,command_id:key}
    try{
      const job=await realtime.submit(d.id,key,action)
      if(session!==sessionEpoch)return
      showSubmission(job,d,action);pendingKeys.delete(target)
    }catch(e){
      if(session!==sessionEpoch)return
      const detail=e as {jobID?:string;status?:number;message?:string}
      const known=detail.status!==401?realtime.values().find((j:Job)=>j.command_id===key&&j.device_id===d.id):undefined
      if(known){showSubmission(known,d,action);pendingKeys.delete(target);return}
      if(detail.jobID){
        let job:Job
        try{job=await realtime.readJob(detail.jobID)}catch(readError){
          if(session===sessionEpoch)submissions.value[d.id]={state:'error',action,command_id:key,message:'已有相关任务，但详情暂不可用，请刷新状态确认',uncertain:true}
          throw readError
        }
        if(session!==sessionEpoch)return
        pendingKeys.delete(target)
        if(job.device_id===d.id){showSubmission(job,d,action,true);return}
        submissions.value[d.id]={state:'error',action,command_id:key,message:'同一 Mac 目标正在执行其他设备的任务，请稍后重试'}
        throw new Error('同一 Mac 目标已有其他任务，请稍后重试')
      }
      submissions.value[d.id]={state:'error',action,command_id:key,message:detail.message||'请求未完成，请刷新状态确认结果',uncertain:!detail.status||detail.status>=500}
      if(detail.status&&detail.status<500)pendingKeys.delete(target)
      throw e
    }
  })
}
async function cancelJob(){await guarded(async()=>{if(!selected.value)return;await request('/wake-jobs/'+selected.value.job_id+'/cancel',{method:'POST'});await openJob(selected.value.job_id)})}
async function clearGuard(d:Device){if(!confirm('请先确认 Mac 账户和密码正确。解除认证保护后，下一次唤醒将允许再次认证。'))return;await guarded(async()=>{await request('/devices/'+d.id+'/authentication/reset',{method:'POST'});notice.value='认证保护已解除'})}
async function saveSettings(){await guarded(async()=>{if(!settings.value)return;const cfg={...settings.value,allowed_networks:networks.value.split('\n').map(s=>s.trim()).filter(Boolean)};const result=await request('/settings',{method:'PUT',body:JSON.stringify(cfg)});savedDeviceDefaults.value={...cfg.device_defaults};notice.value=result.restart_required?'设置已保存；默认参数和记录上限立即生效，监听地址、并发限制或保留天数等变更需重启服务。':'设置已保存并生效。'})}
async function exportDevices(){await guarded(async()=>{const data=await request('/devices/export');const url=URL.createObjectURL(new Blob([JSON.stringify(data,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='remotewake-devices.json';a.click();URL.revokeObjectURL(url)})}
async function importDevices(commit=false){await guarded(async()=>{const data=JSON.parse(importText.value);await request('/devices/import',{method:'POST',body:JSON.stringify({devices:data.devices||data,revision:revision.value,commit})});importPreview.value=!commit;if(commit){importText.value='';await loadDevices();notice.value='设备已导入；Mac 密码需要单独配置'}else notice.value='校验通过，确认后将新增设备'})}
function toggleAll(){if(allChosen.value){const visible=new Set(selectable.value.map(d=>d.id));chosen.value=chosen.value.filter(id=>!visible.has(id))}else chosen.value=[...new Set([...chosen.value,...selectable.value.map(d=>d.id)])].slice(0,50)}
async function continueBatch(){
  if(batchRunning.value||busy.value)return
  if(!realtime.connected()){error.value='请等待实时连接恢复，或点击刷新状态后再继续';return}
  const guard=createBatchGuard({generation:()=>realtime.generation,session:()=>sessionEpoch})
  const owner=sessionEpoch
  resetBatchPanel();batchRunning.value=true;batchStop.value=false;error.value='';batchMessage.value=''
  try {
    const result=await runBatch(batchRows.value,{
      submit:(id:string,key:string)=>realtime.submit(id,key),
      getJob:(id:string)=>realtime.readJob(id),
      waitForJob:(job:Job,update:(job:Job)=>void)=>{guard.assertCurrent();return realtime.waitForJob(job,update)},
      shouldStop:()=>guard.interrupted()||batchStop.value||stopped||!authenticated.value||!realtime.connected(),
      onUpdate:(row:BatchRow)=>{if(owner===sessionEpoch&&batchFinished(row.status)&&pendingKeys.get(row.device_id+':wake')===row.command_id)pendingKeys.delete(row.device_id+':wake')},
    })
    batchMessage.value=result.message||(result.paused?'批次已暂停，请继续确认已有任务结果。':'本批次已处理完成，请查看各设备结果。')
    if(owner===sessionEpoch){
      const finished=batchRows.value.every(r=>batchFinished(r.status))
      if(result.paused||finished&&!batchCompletionNotified){
        const item=batchNotification(batchRows.value,result.paused)
        if(item)toastQueue.show({...item,key:batchNoticeKey})
        if(finished)batchCompletionNotified=true
      }
    }
  }catch(e){report(e)}finally{batchRunning.value=false}
}
async function startBatch(){
  if(batchRunning.value||batchPending.value||!chosen.value.length)return
  const targets=devices.value.filter(d=>chosen.value.includes(d.id))
  if(!batchConfirming.value){batchConfirming.value=true;return}
  batchConfirming.value=false
  batchCompletionNotified=false;batchNoticeKey=commandKey()
  manualAfterBatch.value.clear()
  for(const d of targets)delete submissions.value[d.id]
  batchRows.value=targets.map(d=>{const mapKey=d.id+':wake';const key=pendingKeys.get(mapKey)||commandKey();pendingKeys.set(mapKey,key);return {device_id:d.id,name:d.name,command_id:key,status:'pending',message:'等待提交'}})
  await continueBatch()
}
function batchRowStatus(row:BatchRow){return row.status==='pending'&&!row.job_id?'未提交':statusName(row.status)}
onMounted(()=>{document.addEventListener('visibilitychange',onVisibility);boot()});onUnmounted(()=>{stopped=true;realtime.stop();deviceSync.cancel();toastQueue.clear();jobNotifications.clear();if(clockTimer)clearTimeout(clockTimer);document.removeEventListener('visibilitychange',onVisibility);document.body.classList.remove('modal-open')})
</script>

<template>
  <div v-if="loading" class="loading">正在连接 Gateway…</div>
  <main v-else-if="!authenticated" class="login-layout">
    <div class="login-brand"><div class="brand-mark">R<span>↗</span></div><h1>RemoteWake<span>Gateway</span></h1><p>让远方的设备，随时就绪。</p><div class="login-network"><span>你的浏览器</span><i></i><span>家庭网关</span><i></i><span>你的设备</span></div></div>
    <form class="login-card" @submit.prevent="login"><span class="eyebrow">LOCAL GATEWAY</span><h2>{{ initialized?'欢迎回来':'设置你的 Gateway' }}</h2><p class="muted">{{ initialized?'登录以管理家庭网络中的设备。':'使用本机初始化令牌，创建管理员密码。' }}</p><label v-if="!initialized">初始化令牌<input v-model="setupToken" autocomplete="off" required placeholder="运行 remotewake-gateway token 获取"></label><label>管理员密码<input v-model="password" type="password" :autocomplete="initialized?'current-password':'new-password'" :minlength="initialized?1:6" maxlength="72" required placeholder="输入管理员密码"></label><p v-if="error" class="alert error" role="alert">{{ error }}</p><button class="primary full" :disabled="busy">{{ busy?'正在处理…':initialized?'登录 Gateway':'创建管理员并登录' }} <span>→</span></button><small>通过家庭局域网或蒲公英网络访问</small></form>
  </main>
  <div v-else class="shell">
    <aside class="sidebar"><a class="brand" href="#" @click.prevent="page='devices'"><div class="brand-mark small">R<span>↗</span></div><div>RemoteWake<small>GATEWAY</small></div></a><div class="sidebar-label">工作空间</div><nav><button :class="{chosen:page==='devices'}" @click="page='devices'">▦ <span>设备管理</span><b>{{ devices.length }}</b></button><button :class="{chosen:page==='history'}" @click="page='history'">◷ <span>唤醒记录</span></button><button :class="{chosen:page==='settings'}" @click="page='settings'">⚙ <span>系统设置</span></button></nav><div class="sidebar-bottom"><div class="gateway-status"><i></i><div>{{ gateway.name }}<small>{{ gateway.version }}</small></div></div><button class="logout" @click="logout">退出登录 ↗</button></div></aside>
    <div class="workspace"><header class="topbar"><span>家庭网络 <span class="slash">/</span> {{ page==='devices'?'设备管理':page==='history'?'唤醒记录':'系统设置' }}</span><span class="connection" :class="{degraded:realtimeStatus!=='connected'}"><i></i><span :title="connectionLabel">{{ connectionLabel }}</span><button class="text-button" :disabled="busy" @click="refreshStatus">刷新状态</button></span></header>
      <div class="content">
      <template v-if="page==='devices'"><div class="page-heading"><div><span class="eyebrow">YOUR CONNECTED HOME</span><h1>设备管理<span class="count">{{ devices.length }}</span></h1><p class="muted">管理你的设备，随时发起远程唤醒。</p></div><button class="primary" @click="newDevice">＋ 添加设备</button></div>
        <div class="stats"><div><span>已管理设备</span><strong>{{ devices.length.toString().padStart(2,'0') }}</strong><small>家庭网络中的设备</small></div><div><span>正在执行</span><strong>{{ activeCount.toString().padStart(2,'0') }}<i class="stat-dot"></i></strong><small>唤醒与检测任务</small></div><div><span>唤醒方式</span><strong>03</strong><small>WoL · Mac VNC · 组合唤醒</small></div></div>
        <div class="list-toolbar"><h2>全部设备</h2><label class="search"><span>⌕</span><input v-model="search" placeholder="搜索设备名称或 IP" aria-label="搜索设备"></label></div>
        <div v-if="devices.length" class="batch-toolbar"><label class="checkbox"><input type="checkbox" :checked="allChosen" :indeterminate="someChosen" :disabled="batchRunning||!selectable.length" @change="toggleAll">全选当前结果（最多 50 台）</label><span class="muted">已选 {{ chosen.length }} 台</span><button class="text-button" :disabled="batchRunning||!chosen.length" @click="chosen=[]">清空选择</button><button class="primary" :disabled="busy||batchRunning||batchPending||batchConfirming||!chosen.length" @click="startBatch">唤醒所选</button></div>
        <section v-if="batchConfirming" class="batch-confirm" aria-label="确认批量唤醒"><div><strong>确认唤醒所选 {{ chosen.length }} 台设备？</strong><p>{{ devices.filter(d=>chosen.includes(d.id)).map(d=>d.name).join('、') }}</p><small>将逐台创建任务。开始后可停止后续提交，已发送的请求无法撤销。</small></div><div class="button-row"><button class="secondary" @click="batchConfirming=false">取消批量操作</button><button class="primary" :disabled="!chosen.length" @click="startBatch">确认唤醒 {{ chosen.length }} 台</button></div></section>
        <section v-if="batchRows.length && batchVisible" class="batch-panel" :class="{'is-collapsed':batchCollapsed}" aria-label="批量唤醒结果">
          <div class="batch-heading">
            <div class="batch-summary"><h2>批量唤醒</h2><span role="status">{{ batchSummary.completed }} / {{ batchSummary.total }} 已处理 · 成功 {{ batchSummary.success }}<span v-if="batchSummary.issues" class="batch-issues"> · 异常 {{ batchSummary.issues }}</span></span></div>
            <div class="batch-actions">
              <button v-if="batchRunning" class="secondary" :disabled="batchStop" @click="batchStop=true;batchMessage='将停止后续提交，当前任务会继续执行。'">{{ batchStop?'正在停止后续提交':'停止后续' }}</button>
              <button v-else-if="batchPending" class="primary" :disabled="busy" @click="continueBatch">继续未完成</button>
              <button class="text-button" :aria-expanded="!batchCollapsed" aria-controls="batch-result-details" @click="toggleBatchPanel">{{ batchCollapsed?'展开详情':'收起详情' }}</button>
              <button v-if="!batchRunning && !batchPending" class="batch-close" aria-label="关闭结果" title="关闭结果，任务记录仍可在唤醒记录中查看" @click="closeBatchPanel">×</button>
            </div>
          </div>
          <div v-show="!batchCollapsed" id="batch-result-details" class="batch-details">
            <p class="muted">逐台执行，沿用已有任务保护。连接中断后使用“继续未完成”确认结果。全部成功后 5 秒自动收起，任务记录可在“唤醒记录”查看。</p>
            <p v-if="batchMessage" role="status">{{ batchMessage }}</p>
            <div class="batch-results"><div v-for="row in batchRows" :key="row.command_id" class="batch-result"><strong>{{ row.name }}</strong><span class="badge" :class="row.status">{{ batchRowStatus(row) }}</span><p>{{ row.message }}</p><button v-if="row.job_id" class="text-button" @click="guarded(async()=>{await openJob(row.job_id)})">详情 →</button></div></div>
          </div>
        </section>
        <div v-if="!devices.length" class="empty"><div class="empty-icon">⌘</div><h2>连接你的第一台设备</h2><p>添加 PC、Mac 或 NAS，开始使用远程唤醒。</p><button class="primary" @click="newDevice">＋ 添加设备</button></div>
        <div v-else class="device-grid"><article v-for="d in filtered" :key="d.id" class="device-card"><div class="device-top"><label class="device-select"><input v-model="chosen" type="checkbox" :value="d.id" :aria-label="'选择 '+d.name" :disabled="batchRunning||!canWake(d)||(chosen.length>=50&&!chosen.includes(d.id))"></label><div class="device-icon" :class="d.type">{{ d.type==='mac'?'⌘':d.type==='nas'?'▤':'▣' }}</div><span class="badge" :class="d.active_job_id?'running':''">{{ d.active_job_id?'任务进行中':!d.enabled?'已禁用':observation(d.id) }}</span></div><h3>{{ d.name }}</h3><dl><div><dt>目标 IP</dt><dd>{{ d.ip||'未配置' }}</dd></div><div><dt>唤醒方式</dt><dd>{{ methodName(d.wake_method) }}</dd></div><div><dt>{{ d.wake_method==='mac_vnc'?'VNC 账户':'MAC 地址' }}</dt><dd>{{ d.wake_method==='mac_vnc'?d.vnc.username:d.wol.mac }}</dd></div></dl><div v-if="d.wake_method!=='wol'&&!d.password_configured" class="credential-hint">请先编辑设备，配置 Mac 密码</div><div class="device-actions"><button class="wake-button" :disabled="busy||batchRunning||!d.enabled||!!d.active_job_id" @click="submit(d)">{{ d.active_job_id?'◌ 执行中':'ϟ 唤醒设备' }}</button><button class="icon-button" title="TCP 检测" :disabled="busy||batchRunning||!!d.active_job_id" @click="submit(d,'probe')">⌁</button><button class="icon-button" title="编辑设备" :disabled="batchRunning||!!d.active_job_id" @click="edit(d)">✎</button></div><DeviceActivity :device="d" :job="activityJob(d)" :submission="submissionFor(d)" :connected="realtimeStatus==='connected'" @details="id=>guarded(()=>openJob(id))" /></article></div>
        <p class="footnote">ⓘ 唤醒包已发送或 VNC 认证成功不代表物理屏幕已亮；端口状态在检测后 60 秒内有效。</p>
      </template>
      <template v-else-if="page==='history'"><div class="page-heading"><div><span class="eyebrow">ACTIVITY</span><h1>唤醒记录</h1><p class="muted">追踪每次任务的执行步骤和结果。</p></div><button class="secondary" :disabled="busy||historyLoading" @click="guarded(()=>loadJobs())">{{ historyLoading?'正在加载…':'刷新记录' }}</button></div><p v-if="historyDirty" class="history-update" role="status">记录有变化，可点击“刷新记录”查看。</p><div class="table-wrap"><table><thead><tr><th>设备</th><th>操作</th><th>创建时间</th><th>结果</th><th></th></tr></thead><tbody><tr v-for="j in jobs" :key="j.job_id"><td>{{ devices.find(d=>d.id===j.device_id)?.name||j.device_id }}<small>{{ j.command_id }}</small></td><td>{{ j.action==='probe'?'TCP 检测':'远程唤醒' }}</td><td>{{ date(j.created_at) }}</td><td><span class="badge" :class="j.status">{{ statusName(j.status) }}</span></td><td><button class="text-button" @click="guarded(()=>openJob(j.job_id))">查看详情 →</button></td></tr><tr v-if="!jobs.length"><td colspan="5" class="muted">暂无任务记录</td></tr></tbody></table></div><button v-if="historyCursor" class="secondary load-more" :disabled="busy||historyLoading" @click="guarded(()=>loadJobs(true))">加载更多</button></template>
      <template v-else-if="settings"><div class="page-heading"><div><span class="eyebrow">PREFERENCES</span><h1>系统设置</h1><p class="muted">管理 Gateway 配置和公开设备备份。</p></div></div><form class="settings-panel" @submit.prevent="saveSettings"><h2>Gateway 配置</h2><p v-if="error" class="alert error" role="alert">{{ error }}</p><div class="form-grid"><label>Gateway 名称<input v-model="settings.name" required maxlength="100"></label><label>监听地址<input v-model="settings.listen" required placeholder="127.0.0.1:9000"></label><label class="wide">公开访问地址<input v-model="settings.public_url" placeholder="https://wake.example.com（可选）"><small>使用 HTTPS 反向代理时设置；变更后需重启并重新登录。</small></label><label class="wide">允许访问的目标网段<textarea v-model="networks" rows="3" required></textarea><small>每行一个 CIDR，仅允许向这些网段的设备发送请求。</small></label><label>最大并发任务<input v-model.number="settings.max_concurrent" type="number" min="1" max="16" required></label><label>最大 VNC 并发<input v-model.number="settings.max_vnc" type="number" min="1" :max="settings.max_concurrent" required></label><label>等待队列容量<input v-model.number="settings.queue_size" type="number" min="1" max="128" required></label><label>任务保留天数<input v-model.number="settings.retention_days" type="number" min="1" max="90" required><small>过期记录在启动及接受新任务前清理，修改后重启生效。</small></label><label>最大任务记录数<input v-model.number="settings.max_jobs" type="number" min="100" max="100000" required><small>保存后立即生效，不能小于现有记录数；达到上限且无过期记录可清理时暂停接受新任务。</small></label></div><fieldset class="default-settings"><legend>新设备默认参数</legend><p class="muted">仅用于之后添加的设备，保存后立即生效，不覆盖已有设备。</p><div class="form-grid"><label class="wide">默认广播地址<input v-model="settings.device_defaults.broadcast" required placeholder="255.255.255.255"></label><label>默认 UDP 端口<input v-model.number="settings.device_defaults.port" type="number" min="1" max="65535" required></label><label>默认发送次数<input v-model.number="settings.device_defaults.repeat" type="number" min="1" max="10" required></label></div></fieldset><button class="primary" :disabled="busy">保存设置</button></form><section class="settings-panel history-cleanup"><h2>手动清理任务记录</h2><p class="muted">删除对应的历史任务文件并释放存储空间。执行中、排队中及防重复执行保护期内的记录会保留；保护期至命令过期后 1 分钟结束。</p><label>清理范围<select v-model="cleanupScope" :disabled="busy" @change="cleanupPreview=null"><option value="expired">仅清理过期记录</option><option value="completed">清理全部已结束记录</option></select></label><p class="muted">过期范围按当前已生效的保留天数计算，未保存或待重启的设置不参与本次清理。</p><button class="secondary" :disabled="busy||batchRunning" @click="previewCleanup">预览清理</button><div v-if="cleanupPreview" class="cleanup-confirm" role="region" aria-label="确认清理任务记录"><strong>将永久删除 {{ cleanupPreview.eligible }} 条{{ cleanupPreview.scope==='expired'?'过期':'已结束' }}记录</strong><p>保留活动任务 {{ cleanupPreview.active }} 条、保护期内记录 {{ cleanupPreview.protected }} 条。{{ cleanupPreview.scope==='expired'?'当前保留天数为 '+cleanupPreview.retention_days+' 天。':'' }}</p><p>删除后无法恢复，并会移除对应的命令去重记录，请勿重新提交这些旧请求。设备配置、密码和认证保护不受影响。</p><div class="button-row"><button class="secondary" :disabled="busy" @click="cleanupPreview=null">取消清理</button><button class="secondary danger" :disabled="busy||!cleanupPreview.eligible" @click="confirmCleanup">确认删除 {{ cleanupPreview.eligible }} 条记录</button></div></div></section><section class="settings-panel"><h2>设备导入与导出</h2><p class="muted">仅包含公开配置，不含管理员密码或 VNC 密码。导入只新增设备，重复 ID 会被拒绝。</p><button class="secondary" @click="exportDevices">↓ 导出设备 JSON</button><label>导入 JSON<textarea v-model="importText" rows="6" placeholder="粘贴导出的设备 JSON" @input="importPreview=false"></textarea></label><div class="button-row"><button class="secondary" :disabled="busy||!importText" @click="importDevices(false)">检查导入</button><button v-if="importPreview" class="primary" :disabled="busy" @click="importDevices(true)">确认新增设备</button></div></section></template>
      </div><footer>RemoteWake Gateway <span>本地管理 · 数据保存在你的 Gateway</span></footer>
    </div>
    <div v-if="editing" class="modal-backdrop device-backdrop">
      <section class="modal device-modal" role="dialog" aria-modal="true" aria-labelledby="edit-title">
        <header class="device-modal-header">
          <div><span class="eyebrow">DEVICE CONFIGURATION</span><h2 id="edit-title">{{ isNew?'添加设备':'编辑设备' }}</h2></div>
          <button type="button" class="icon-button" aria-label="关闭" :disabled="busy" @click="editing=null">×</button>
        </header>
        <form class="device-form" @submit.prevent="saveDevice">
          <div class="modal-body" tabindex="0" aria-label="设备配置表单">
            <div class="form-grid">
              <label class="wide">设备名称<input v-model="editing.name" required maxlength="100" autofocus></label>
              <label>设备类型<select v-model="editing.type"><option value="pc">PC / Linux</option><option value="mac">Mac</option><option value="nas">NAS</option></select></label>
              <label>唤醒方式<select v-model="editing.wake_method"><option value="wol">WoL 魔术包</option><option value="mac_vnc">Mac 屏幕共享（VNC）</option><option value="wol_mac_vnc">WoL + Mac 屏幕共享</option></select></label>
              <label v-if="editing.wake_method!=='wol'" class="wide">目标 IPv4 地址<input v-model="editing.ip" required placeholder="192.168.1.100"></label>
            </div>
            <fieldset v-if="editing.wake_method!=='mac_vnc'">
              <legend>WoL 设置</legend>
              <div class="form-grid">
                <label class="wide">MAC 地址<input v-model="editing.wol.mac" required placeholder="AA:BB:CC:DD:EE:FF"></label>
                <label>发送网卡
                  <select v-model="editing.wol.interface" aria-describedby="wol-interface-help" @change="chooseInterface">
                    <option value="">由系统选择</option>
                    <option v-if="editing.wol.interface && !sendInterfaces.some(i=>i.name===editing?.wol.interface)" :value="editing.wol.interface">未找到：{{ editing.wol.interface }}</option>
                    <option v-for="i in sendInterfaces" :key="i.name" :value="i.name">{{ i.label }}</option>
                  </select>
                </label>
                <label>广播地址<input v-model="editing.wol.broadcast" required placeholder="192.168.1.255 或 255.255.255.255"></label>
                <label>UDP 端口<input v-model.number="editing.wol.port" type="number" min="1" max="65535" required></label>
                <label>发送次数<input v-model.number="editing.wol.repeat" type="number" min="1" max="10" required></label>
                <p id="wol-interface-help" class="hint wide">默认由系统路由选择发送路径。手动选择网卡后会建议广播地址，可按实际网络修改。</p>
                <div v-if="broadcastSuggestions.length" class="broadcast-suggestions wide"><span>所选网卡的广播地址：</span><button v-for="value in broadcastSuggestions" :key="value" type="button" class="text-button" :aria-label="'使用广播地址 '+value" @click="editing.wol.broadcast=value">{{ value }}</button></div>
                <div v-if="broadcastHints.length" class="network-warnings wide" role="status" aria-live="polite"><p v-for="hint in broadcastHints" :key="hint">{{ hint }}</p></div>
              </div>
            </fieldset>
            <fieldset v-if="editing.wake_method!=='wol'">
              <legend>Mac 屏幕共享</legend>
              <p class="hint">请在 Mac 上开启屏幕共享和网络唤醒，允许此账户访问。仅用于可信网络，不提供远程桌面画面。</p>
              <div class="form-grid">
                <label>Mac 用户名<input v-model="editing.vnc.username" required autocomplete="off"></label>
                <label>VNC 端口<input v-model.number="editing.vnc.port" type="number" min="1" max="65535" required></label>
                <label class="wide"><span>Mac 密码 <small class="muted">{{ editing.password_configured?'已配置；留空保留':'尚未配置' }}</small></span><input v-model="vncPassword" type="password" autocomplete="new-password" :disabled="clearPassword" placeholder="只保存到 Gateway 本地，不提供查看"></label>
                <label class="checkbox wide"><input v-model="clearPassword" type="checkbox" @change="vncPassword=''">清除已保存的密码</label>
                <label>认证后保持（秒）<input v-model.number="editing.vnc.hold_seconds" type="number" min="0" max="30" required></label>
              </div>
              <button v-if="!isNew" type="button" class="text-button" @click="clearGuard(editing)">检查账户后解除认证保护</button>
            </fieldset>
            <details class="optional-fields" :open="optionalOpen" @toggle="optionalOpen=($event.target as HTMLDetailsElement).open">
              <summary>{{ editing.wake_method==='wol'?'上线检测与备注（可选）':'备注（可选）' }}</summary>
              <div class="optional-content">
                <template v-if="editing.wake_method==='wol'">
                  <label>目标 IPv4 地址<input v-model="editing.ip" :required="editing.probe.enabled" placeholder="192.168.1.100"></label>
                  <label class="checkbox"><input v-model="editing.probe.enabled" type="checkbox">发送 WoL 后等待 TCP 端口可达</label>
                  <div v-if="editing.probe.enabled" class="form-grid"><label>TCP 端口<input v-model.number="editing.probe.port" type="number" min="1" max="65535" required></label><label>最长等待（秒）<input v-model.number="editing.probe.timeout_seconds" type="number" min="1" max="60" required></label></div>
                </template>
                <label>备注<textarea v-model="editing.note" rows="2" maxlength="2000" placeholder="可选，记录设备位置或用途"></textarea></label>
              </div>
            </details>
            <label class="checkbox device-enabled"><input v-model="editing.enabled" type="checkbox">启用此设备</label>
          </div>
          <div class="device-modal-footer">
            <p v-if="error" class="alert error" role="alert">{{ error }}</p>
            <div class="modal-actions">
              <button v-if="!isNew" type="button" class="danger text-button" :disabled="busy" @click="remove(editing)">删除设备</button>
              <span></span>
              <button type="button" class="secondary" :disabled="busy" @click="editing=null">取消</button>
              <button type="submit" class="primary" :disabled="busy">{{ busy?'保存中…':'保存设备' }}</button>
            </div>
          </div>
        </form>
      </section>
    </div>
    <div v-if="selected" class="modal-backdrop" @click.self="selected=null"><section class="modal job-modal" role="dialog" aria-modal="true" aria-labelledby="job-title"><header><div><span class="eyebrow">WAKE ACTIVITY</span><h2 id="job-title">{{ devices.find(d=>d.id===selected?.device_id)?.name||selected.device_id }}</h2></div><button class="icon-button" aria-label="关闭" @click="selected=null">×</button></header><div class="job-summary"><span class="badge" :class="selected.status">{{ statusName(selected.status) }}</span><span>{{ phaseName(selected.phase) }}</span></div><p class="muted">{{ selected.message||'任务在后台执行，关闭页面不会取消。' }}</p><ol class="timeline"><li v-for="(step,i) in selected.steps" :key="i" :class="step.status"><span class="timeline-dot">{{ step.status==='success'?'✓':step.status==='failed'?'!':'◌' }}</span><div><strong>{{ step.name==='wol'?'发送 Magic Packet':step.name==='vnc'?'Mac 屏幕共享认证':'TCP 端口检测' }}</strong><p>{{ step.message||phaseName(selected.phase) }}</p><code v-if="step.status==='failed'">{{ step.code }}</code></div></li></ol><dl class="job-metadata"><div><dt>创建时间</dt><dd>{{ date(selected.created_at) }}</dd></div><div v-if="!active(selected)&&selected.finished_at&&!selected.finished_at.startsWith('0001-')"><dt>完成时间</dt><dd>{{ date(selected.finished_at) }}</dd></div><div><dt>命令 ID</dt><dd>{{ selected.command_id }}</dd></div><div><dt>任务 ID</dt><dd>{{ selected.job_id }}</dd></div></dl><p class="hint">步骤结果不代表物理显示器状态。取消无法撤销已经发送的请求。</p><div class="modal-actions"><button v-if="active(selected)" class="secondary" :disabled="busy" @click="cancelJob">取消任务</button><span></span><button class="primary" @click="selected=null">关闭</button></div></section></div>
  </div>
  <ToastNotice v-if="authenticated" :toast="toast" @dismiss="toastQueue.dismiss" @pause="toastQueue.pause" @resume="toastQueue.resume" />
</template>

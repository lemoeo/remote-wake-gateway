<script setup lang="ts">
import { computed } from 'vue'
import type { Device, Job, SubmissionState } from './types'
import { presentJob } from './job-presentation.mjs'
const props=defineProps<{device:Device;job?:Job;submission?:SubmissionState;connected:boolean}>()
defineEmits<{details:[id:string]}>()
const view=computed(()=>presentJob(props.job,props.device,props.submission))
const waiting=computed(()=>view.value.tone==='idle'&&!view.value.jobID)
const steps=computed(()=>view.value.steps.filter(step=>step.name!=='finish'))
const showSteps=computed(()=>view.value.active&&view.value.tone==='running'&&props.connected&&steps.value.length>0)
const timestamp=computed(()=>view.value.time?new Date(view.value.time).toLocaleTimeString('zh-CN',{hour12:false}):'')
const timeLabel=computed(()=>view.value.finished?'完成于':'创建于')
const fullTime=computed(()=>view.value.time?`${timeLabel.value} ${new Date(view.value.time).toLocaleString('zh-CN',{hour12:false})}`:'')
</script>

<template>
  <section class="device-activity" :class="[view.tone,{'is-waiting':waiting}]" :aria-label="device.name+'任务状态'">
    <div class="activity-layout" :class="{'has-details':view.jobID}" role="status" aria-live="polite" aria-atomic="true">
      <div class="activity-heading">
        <span v-if="!waiting" class="activity-symbol" :class="{'is-spinning':view.active&&view.tone==='running'&&connected}" aria-hidden="true">{{ view.tone==='error'?'×':view.tone==='warning'?'!':view.active?(connected?'':'…'):view.tone==='success'?'✓':'·' }}</span>
        <strong :title="view.title">{{ view.title }}</strong>
      </div>
      <ol v-if="showSteps" class="activity-steps activity-secondary" aria-label="执行步骤">
        <li v-for="step in steps" :key="step.name" :class="step.state" :aria-label="step.label+'：'+({done:'已完成',active:'执行中',failed:'失败',pending:'等待中',skipped:'未执行',uncertain:'结果异常'}[step.state]||step.state)"><span aria-hidden="true">{{ step.state==='done'?'✓':step.state==='failed'?'×':step.state==='uncertain'?'!':step.state==='active'?'●':'○' }}</span>{{ step.label }}</li>
      </ol>
      <p v-else-if="!waiting" class="activity-secondary activity-description" :class="{'activity-sync':view.active&&!connected}" :title="view.active&&!connected?'等待同步 · 可手动刷新':view.detail">{{ view.active&&!connected?'等待同步 · 可手动刷新':view.detail }}</p>
      <time v-if="view.time" class="activity-time" :datetime="view.time" :title="fullTime" :aria-label="fullTime">{{ timeLabel }} {{ timestamp }}</time>
      <button v-if="view.jobID" type="button" class="text-button activity-details" :aria-label="'查看 '+device.name+' 的任务详情'" @click="$emit('details',view.jobID)">详情 →</button>
    </div>
  </section>
</template>

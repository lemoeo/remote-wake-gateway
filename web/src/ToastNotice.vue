<script setup lang="ts">
import { ref, watch } from 'vue'
import type { ToastItem } from './types'
const props=defineProps<{toast:ToastItem|null}>()
const emit=defineEmits(['dismiss','pause','resume'])
const hovered=ref(false), focused=ref(false)
watch(()=>props.toast?.id,()=>{hovered.value=false;focused.value=false},{flush:'sync'})
function updatePause(){emit(hovered.value||focused.value?'pause':'resume')}
function focusOut(event:FocusEvent){
  if((event.currentTarget as HTMLElement).contains(event.relatedTarget as Node|null))return
  focused.value=false;updatePause()
}
</script>

<template>
  <div class="toast-viewport" aria-label="操作提示">
    <Transition name="toast">
      <div v-if="toast" :key="toast.id" class="toast-notice" :class="toast.kind" :role="toast.kind==='error'?'alert':'status'" aria-atomic="true" @mouseenter="hovered=true;updatePause()" @mouseleave="hovered=false;updatePause()" @focusin="focused=true;updatePause()" @focusout="focusOut">
        <span class="toast-symbol" aria-hidden="true">{{ toast.kind==='success'?'✓':toast.kind==='error'?'×':toast.kind==='warning'?'!':'i' }}</span>
        <p>{{ toast.message }}</p>
        <button type="button" aria-label="关闭提示" @click="$emit('dismiss')">×</button>
      </div>
    </Transition>
  </div>
</template>

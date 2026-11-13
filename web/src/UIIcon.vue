<script setup lang="ts">
withDefaults(defineProps<{
  name: 'wake' | 'probe' | 'edit' | 'spinner' | 'search' | 'close' | 'refresh' | 'logout'
  size?: number
  spinning?: boolean
}>(), { size: 16, spinning: false })
</script>

<template>
  <svg class="ui-icon" :class="{'is-spinning':spinning||name==='spinner'}" :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <path v-if="name==='wake'" d="m13 2-9 12h7l-1 8 10-12h-7l1-8Z" />
    <path v-else-if="name==='probe'" d="M2 12h4l3-8 6 16 3-8h4" />
    <template v-else-if="name==='edit'">
      <path d="m15 5 4 4M4 16l-1 5 5-1L21 7a2.8 2.8 0 0 0-4-4L4 16Z" />
    </template>
    <path v-else-if="name==='spinner'" d="M20.5 12a8.5 8.5 0 1 1-5.9-8.1" />
    <template v-else-if="name==='search'">
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="m16 16 5 5" />
    </template>
    <path v-else-if="name==='close'" d="m6 6 12 12M18 6 6 18" />
    <template v-else-if="name==='refresh'">
      <path d="M20 7v5h-5M4 17v-5h5" />
      <path d="M6.1 6.1A8 8 0 0 1 20 12M4 12a8 8 0 0 0 13.9 5.9" />
    </template>
    <template v-else-if="name==='logout'">
      <path d="M9 4H5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4M9 12h12m-4-4 4 4-4 4" />
    </template>
  </svg>
</template>

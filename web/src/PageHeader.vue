<script setup lang="ts">
import UIIcon from './UIIcon.vue'

defineProps<{
  eyebrow: string
  title: string
  description: string
  connection: { label: string; text: string; degraded: boolean; refreshing: boolean; busy: boolean }
}>()
defineEmits<{ refresh: [] }>()
</script>

<template>
  <header class="page-heading">
    <div class="page-heading-meta">
      <span class="eyebrow">{{ eyebrow }}</span>
      <span class="connection" :class="{degraded:connection.degraded}">
        <i aria-hidden="true"></i>
        <span :title="connection.label" role="status" :aria-label="connection.label">{{ connection.text }}</span>
        <button class="text-button refresh-button" title="刷新状态" aria-label="刷新设备和任务状态" :aria-busy="connection.refreshing" :disabled="connection.busy" @click="$emit('refresh')"><UIIcon name="refresh" :spinning="connection.refreshing" /></button>
      </span>
    </div>
    <h1>{{ title }}</h1>
    <div v-if="$slots.default" class="page-heading-actions"><slot /></div>
    <p class="muted page-description">{{ description }}</p>
  </header>
</template>

<style scoped>
.page-heading {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 6px 16px;
  margin-bottom: 24px;
}

.page-heading-meta {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
}

.page-heading-meta > .eyebrow {
  min-width: 0;
}

.page-heading .connection {
  flex-shrink: 0;
  gap: 6px;
  color: var(--text-muted);
}

.page-heading .connection.degraded {
  color: #8d6a24;
}

.page-heading .refresh-button {
  width: 44px;
  height: 44px;
  padding: 12px;
}

.page-heading > h1 {
  grid-column: 1;
  grid-row: 2;
  min-width: 0;
  margin: 0;
}

.page-heading-actions {
  grid-column: 2;
  grid-row: 2;
  display: flex;
  align-items: center;
  gap: 8px;
}

.page-description {
  grid-column: 1 / -1;
  margin: 0;
}

@media (max-width: 720px) {
  .page-heading {
    column-gap: 12px;
  }
}
</style>

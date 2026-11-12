// Keep a creation key for the lifetime of the form, including uncertain HTTP
// failures. Editing the draft after a lost response must not create a new ID.
export function createDeviceEditor({ request, commandKey, confirmDelete }) {
  let createKey = ''
  return {
    begin() { createKey = commandKey() },
    save(device, isNew) {
      return request('/devices' + (isNew ? '' : '/' + device.id), {
        method: isNew ? 'POST' : 'PUT',
        ...(isNew ? { headers: { 'Idempotency-Key': createKey } } : {}),
        body: JSON.stringify(device),
      })
    },
    async remove(device, onDeleted) {
      if (!confirmDelete(`删除“${device.name}”？历史任务会保留。`)) return false
      await request('/devices/' + device.id, { method: 'DELETE', headers: { 'If-Match': String(device.revision) } })
      onDeleted(device.id)
      return true
    },
  }
}

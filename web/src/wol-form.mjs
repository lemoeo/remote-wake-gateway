// One selection per NIC, even when it owns several IPv4 addresses.
export function interfaceOptions(networks) {
  const groups = new Map()
  for (const network of networks) {
    const ips = groups.get(network.name) || []
    if (!ips.includes(network.ip)) ips.push(network.ip)
    groups.set(network.name, ips)
  }
  return Array.from(groups, ([name, ips]) => ({ name, label: `${name} · ${ips.join(', ')}` }))
}

export function suggestedBroadcasts(networks, name) {
  return [...new Set(networks.filter(n => n.name === name).map(n => n.broadcast))]
}

export function broadcastOnSelection(networks, name, current) {
  if (!name || current === '255.255.255.255') return current
  const suggestions = suggestedBroadcasts(networks, name)
  return suggestions.includes(current) ? current : suggestions[0] || current
}

function validIPv4(value) {
  return /^(?:0|[1-9]\d{0,2})(?:\.(?:0|[1-9]\d{0,2})){3}$/.test(value) && value.split('.').every(n => Number(n) <= 255)
}

// These hints do not block saving. The backend validates destination policy and
// checks a manually selected NIC again when executing the wake request.
export function broadcastWarnings(networks, name, broadcast) {
  const selected = networks.filter(n => n.name === name)
  if (name && !selected.length) return ['所选发送网卡当前不可用或没有 IPv4 地址，执行唤醒时会失败；请重新选择。']
  if (!validIPv4(broadcast)) return []
  if (broadcast === '255.255.255.255') {
    const nonLoopback = networks.filter(n => !n.ip.startsWith('127.'))
    if (!name && new Set(nonLoopback.map(n => n.name)).size > 1) {
      return ['当前使用 255.255.255.255，且存在多个可用网卡。建议明确选择目标 LAN 的发送网卡，避免系统选择其他发送路径。']
    }
    return []
  }
  if (name && !selected.some(n => n.broadcast === broadcast)) {
    return [`广播地址与所选网卡的子网广播地址不一致（建议：${suggestedBroadcasts(networks, name).join('、')}）。仍可保存，请确认网络路由支持此目标。`]
  }
  if (!name && !networks.some(n => n.broadcast === broadcast)) {
    return ['未找到与该广播地址匹配的本机子网。仍可保存，发送路径将由系统路由决定。']
  }
  return []
}

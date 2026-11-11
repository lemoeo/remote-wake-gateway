import test from 'node:test'
import assert from 'node:assert/strict'
import { interfaceOptions, broadcastOnSelection, broadcastWarnings } from '../src/wol-form.mjs'

const networks = [
  {name:'eth0', ip:'192.168.1.10', broadcast:'192.168.1.255'},
  {name:'eth0', ip:'192.168.2.10', broadcast:'192.168.2.255'},
  {name:'br-lan', ip:'10.0.0.1', broadcast:'10.0.0.255'},
]
test('one NIC remains one option when it has multiple IPv4 addresses', () => {
  assert.deepEqual(interfaceOptions(networks), [
    {name:'eth0',label:'eth0 · 192.168.1.10, 192.168.2.10'},
    {name:'br-lan',label:'br-lan · 10.0.0.1'},
  ])
})
test('selecting a NIC suggests its broadcast while preserving limited broadcast and valid aliases', () => {
  assert.equal(broadcastOnSelection(networks,'eth0',''), '192.168.1.255')
  assert.equal(broadcastOnSelection(networks,'eth0','192.168.2.255'), '192.168.2.255')
  assert.equal(broadcastOnSelection(networks,'eth0','255.255.255.255'), '255.255.255.255')
  assert.equal(broadcastOnSelection(networks,'','192.168.2.255'), '192.168.2.255')
})
test('custom broadcast gets an advisory without replacing the user input', () => {
  assert.match(broadcastWarnings(networks,'eth0','10.0.0.255')[0], /仍可保存/)
  assert.deepEqual(broadcastWarnings(networks,'eth0','192.168.2.255'), [])
  assert.match(broadcastWarnings(networks,'','172.16.1.255')[0], /系统路由/)
})
test('limited broadcast warns about multiple NICs only when system selection is used', () => {
  assert.match(broadcastWarnings(networks,'','255.255.255.255')[0], /多个可用网卡/)
  assert.deepEqual(broadcastWarnings(networks,'eth0','255.255.255.255'), [])
  assert.deepEqual(broadcastWarnings(networks.filter(n=>n.name==='eth0'),'','255.255.255.255'), [])
})
test('missing explicit NIC is reported and malformed address does not get a subnet hint', () => {
  assert.match(broadcastWarnings(networks,'missing','192.168.1.255')[0], /执行唤醒时会失败/)
  assert.deepEqual(broadcastWarnings(networks,'eth0','999.1.1.1'), [])
})

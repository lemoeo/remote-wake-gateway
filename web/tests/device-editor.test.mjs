import test from 'node:test'
import assert from 'node:assert/strict'
import { createDeviceEditor } from '../src/device-editor.mjs'

test('cancelling delete makes no request and retains the editor draft',async()=>{
  let closed=false,requests=0
  const editor=createDeviceEditor({request:async()=>requests++,commandKey:()=>'',confirmDelete:()=>false})
  assert.equal(await editor.remove({id:'pc',name:'draft',revision:2},()=>closed=true),false)
  assert.equal(closed,false);assert.equal(requests,0)
})
test('only a successful delete closes the matching draft',async()=>{
  let deleted=''
  const calls=[]
  const editor=createDeviceEditor({request:async(path,options)=>calls.push({path,options}),commandKey:()=>'',confirmDelete:()=>true})
  assert.equal(await editor.remove({id:'pc',name:'PC',revision:3},id=>deleted=id),true)
  assert.equal(deleted,'pc');assert.equal(calls[0].options.headers['If-Match'],'3')
  const failure=createDeviceEditor({request:async()=>{throw Error('delete failed')},commandKey:()=>'',confirmDelete:()=>true})
  deleted='';await assert.rejects(failure.remove({id:'pc',name:'PC'},id=>deleted=id),/delete failed/)
  assert.equal(deleted,'')
})
test('an uncertain create and edited draft retry keep the same key; new forms use another',async()=>{
  const calls=[];let next=0
  const editor=createDeviceEditor({request:async(path,options)=>{calls.push({path,options});if(calls.length===1)throw Error('response lost');return {id:'saved'}},commandKey:()=>`create_${++next}`,confirmDelete:()=>false})
  editor.begin()
  await assert.rejects(editor.save({name:'PC'},true),/response lost/)
  await editor.save({name:'PC'},true)
  await editor.save({name:'edited after response loss'},true)
  editor.begin();await editor.save({name:'other'},true)
  assert.deepEqual(calls.map(c=>c.options.headers['Idempotency-Key']),['create_1','create_1','create_1','create_2'])
  await editor.save({id:'saved',name:'edit'},false)
  assert.equal(calls.at(-1).options.method,'PUT');assert.equal(calls.at(-1).options.headers,undefined)
})

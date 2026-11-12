import test from 'node:test'
import assert from 'node:assert/strict'
import { createToasts } from '../src/toasts.mjs'

function setup(t){
  t.mock.timers.enable({apis:['setTimeout','Date']})
  let current
  const toasts=createToasts({onChange:toast=>current=toast})
  t.after(toasts.clear)
  return {toasts,get current(){return current}}
}
test('submit, success and error notices expire after two, three and eight seconds',t=>{
  const state=setup(t)
  for(const [kind,duration] of [['info',2000],['success',3000],['error',8000]]){
    state.toasts.show({kind,message:kind});t.mock.timers.tick(duration-1)
    assert.equal(state.current.message,kind);t.mock.timers.tick(1);assert.equal(state.current,null)
  }
})
test('completion replaces the same task notice and errors preempt ordinary success',t=>{
  const state=setup(t)
  state.toasts.show({key:'j1',message:'submitted'})
  const id=state.current.id
  state.toasts.show({key:'j1',kind:'success',message:'finished'})
  assert.equal(state.current.id,id);assert.equal(state.current.message,'finished')
  state.toasts.show({key:'j2',kind:'error',message:'failed'})
  assert.equal(state.current.message,'failed')
  state.toasts.show({key:'notice',kind:'success',message:'settings saved'})
  assert.equal(state.current.message,'failed')
  t.mock.timers.tick(8000);assert.equal(state.current.message,'settings saved')
})
test('hover or keyboard focus pauses dismissal and clear removes queued session notices',t=>{
  const state=setup(t)
  state.toasts.show({message:'reading'});t.mock.timers.tick(500);state.toasts.pause()
  t.mock.timers.tick(10000);assert.equal(state.current.message,'reading')
  state.toasts.resume();t.mock.timers.tick(1499);assert.ok(state.current)
  t.mock.timers.tick(1);assert.equal(state.current,null)
  state.toasts.show({message:'current'});state.toasts.show({message:'queued'});state.toasts.clear()
  t.mock.timers.tick(20000);assert.equal(state.current,null)
})
test('queued notices are bounded and replacing a queued task cannot replay a stale submit',t=>{
  const state=setup(t)
  state.toasts.show({key:'blocker',kind:'error',message:'important'})
  state.toasts.show({key:'j1',message:'submitted'})
  state.toasts.show({key:'j1',kind:'success',message:'finished'})
  for(let i=0;i<20;i++)state.toasts.show({key:'n'+i,message:'extra'+i})
  state.toasts.dismiss();assert.equal(state.current.message,'finished')
  const seen=[]
  while(state.current){seen.push(state.current.message);state.toasts.dismiss()}
  assert.ok(seen.length<=5);assert.equal(seen.includes('submitted'),false)
})

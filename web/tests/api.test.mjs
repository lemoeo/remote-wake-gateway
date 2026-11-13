import test from 'node:test'
import assert from 'node:assert/strict'
import { request, setCSRF, commandKey, APIError, serverNow } from '../src/api.mjs'
test('mutation carries CSRF and keeps its idempotency key without retry', async () => {
  const original=globalThis.fetch;let calls=0
  globalThis.fetch=async (path, options)=>{calls++;assert.equal(path,'/api/v1/devices/mac/wake');assert.equal(options.headers['X-CSRF-Token'],'csrf');assert.equal(options.headers['Idempotency-Key'],'same-command');throw new TypeError('connection lost')}
  try {setCSRF('csrf');await assert.rejects(request('/devices/mac/wake',{method:'POST',headers:{'Idempotency-Key':'same-command'}}),/connection lost/);assert.equal(calls,1)} finally {globalThis.fetch=original}
})
test('busy response exposes the existing job for recovery', async()=>{
  const original=globalThis.fetch;globalThis.fetch=async()=>({status:409,ok:false,json:async()=>({error:{code:'device_busy',message:'busy',job_id:'job_existing'}})})
  try{await assert.rejects(request('/devices/mac/wake'),e=>e instanceof APIError&&e.jobID==='job_existing'&&e.status===409)}finally{globalThis.fetch=original}
})
test('keys have a valid unique command format',()=>{const a=commandKey(),b=commandKey();assert.match(a,/^cmd_[a-f0-9]{32}$/);assert.notEqual(a,b)})
test('API responses calibrate observation time from the Gateway Date header',async()=>{
  const original=globalThis.fetch, time=Date.parse('2020-01-01T00:00:00Z')
  globalThis.fetch=async()=>({status:200,ok:true,headers:new Headers({Date:new Date(time).toUTCString()}),json:async()=>({status:'success'})})
  try{assert.deepEqual(await request('/wake-jobs/example'),{status:'success'});assert.ok(Math.abs(serverNow()-time)<2000)}finally{globalThis.fetch=original}
})

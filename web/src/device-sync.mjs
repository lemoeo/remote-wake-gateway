// Every invalidation awaits the coalesced fetch. Rejections must reach the live
// controller so that a connected stream is not mistaken for synchronized data.
export function createDeviceSync({ load, delay = 60 }) {
  let timer, pending, resolve, reject, latest = 0
  function cancel() {
    clearTimeout(timer); timer = undefined; latest = 0
    resolve?.(); pending = resolve = reject = undefined
  }
  function schedule(revision) {
    latest = Math.max(latest, revision)
    if (!pending) pending = new Promise((a, b) => { resolve = a; reject = b })
    clearTimeout(timer)
    timer = setTimeout(async () => {
      const done = resolve, fail = reject, target = latest
      pending = resolve = reject = undefined; timer = undefined; latest = 0
      try { await load(target); done() } catch (error) { fail(error) }
    }, delay)
    return pending
  }
  return { schedule, cancel }
}

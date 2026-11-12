const priority = kind => kind==='error'?3:kind==='warning'?2:1

/** @param {{onChange: Function, now?: Function}} options */
export function createToasts({onChange, now=()=>Date.now()}) {
  let current=null, timer, nextID=0, started=0, remaining=0, paused=false
  const queue=[]
  function arm() {
    clearTimeout(timer)
    if (!current || paused) return
    started=now();timer=setTimeout(dismiss,remaining)
  }
  function activate(item) { current=item;remaining=item.duration;onChange(current);arm() }
  function dismiss() { clearTimeout(timer);current=null;paused=false;onChange(null);if(queue.length)activate(queue.shift()) }
  function show({message,kind='info',key='',duration=kind==='error'||kind==='warning'?8000:kind==='success'?3000:2000}) {
    const item={id:++nextID,message,kind,key,duration}
    const existing=queue.findIndex(t=>key && t.key===key)
    if(existing>=0)queue.splice(existing,1)
    if (current && key && current.key===key) {item.id=current.id;activate(item);return}
    if (!current || !paused && priority(kind)>priority(current.kind)) {activate(item);return}
    queue.push(item);queue.sort((a,b)=>priority(b.kind)-priority(a.kind))
    if(queue.length>5)queue.pop()
  }
  function pause() { if(!current||paused)return;remaining=Math.max(0,remaining-(now()-started));paused=true;clearTimeout(timer) }
  function resume() { if(!paused)return;paused=false;arm() }
  function clear() { clearTimeout(timer);queue.length=0;current=null;paused=false;onChange(null) }
  return {show,dismiss,pause,resume,clear}
}

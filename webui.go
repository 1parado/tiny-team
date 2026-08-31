package main

import (
	"encoding/json"
	"net/http"
)

// RegisterTraceRoutes mounts the live trace UI on mux: "/" serves the
// timeline page and "/api/trace" returns the recorded events as JSON.
func RegisterTraceRoutes(mux *http.ServeMux, tr *Tracer) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(traceHTML))
	})
	mux.HandleFunc("/api/trace", func(w http.ResponseWriter, r *http.Request) {
		events, done := tr.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "done": done})
	})
}

// traceHTML is the single-page timeline UI in an iOS light style: grouped
// white cards on the system background, agent monograms, and delegations
// rendered as nested groups with a purple thread guide. It polls /api/trace
// every 800ms and appends new events as they arrive.
const traceHTML = `<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#F2F2F7">
<title>运行轨迹 · tiny-team</title>
<style>
:root{
  color-scheme:light;
  --bg:#F2F2F7;--card:#FFFFFF;--label:#1C1C1E;--secondary:#8E8E93;--tertiary:#C7C7CC;
  --sep:rgba(60,60,67,.14);--fill:rgba(120,120,128,.10);--code:rgba(120,120,128,.08);
  --blue:#007AFF;--green-text:#248A3D;--red:#FF3B30;--purple:#AF52DE;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--label);
  font:15px/1.55 -apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI","PingFang SC","Helvetica Neue",sans-serif;
  -webkit-font-smoothing:antialiased}
header{position:sticky;top:0;z-index:5;background:rgba(242,242,247,.82);
  -webkit-backdrop-filter:saturate(180%) blur(20px);backdrop-filter:saturate(180%) blur(20px);
  border-bottom:1px solid var(--sep)}
.nav{max-width:680px;margin:0 auto;padding:14px 20px 12px;
  display:flex;align-items:flex-end;justify-content:space-between;gap:12px}
h1{margin:0;font-size:28px;font-weight:800;letter-spacing:-.5px}
.pill{display:inline-flex;align-items:center;gap:6px;padding:5px 12px;border-radius:999px;
  font-size:13px;font-weight:600;white-space:nowrap}
.pill i{width:7px;height:7px;border-radius:50%;background:var(--blue);animation:pulse 1.2s ease-in-out infinite}
.pill.running{background:rgba(0,122,255,.12);color:var(--blue)}
.pill.done{background:rgba(52,199,89,.15);color:var(--green-text)}
@keyframes pulse{50%{opacity:.35}}
main{max-width:680px;margin:0 auto;padding:16px 20px 64px}
.card{position:relative;background:var(--card);border-radius:16px;padding:12px 14px;margin:8px 0;
  border-left:3px solid transparent;
  transition:transform .18s ease,box-shadow .18s ease;
  animation:rise .4s cubic-bezier(.32,.72,0,1)}
.card:hover{transform:translateY(-2px);box-shadow:0 6px 18px rgba(60,60,67,.12)}
.card.k-task{border-left-color:var(--blue)}
.card.k-final{border-left-color:#34C759;background:rgba(52,199,89,.08)}
.card.k-error{border-left-color:var(--red);background:rgba(255,59,48,.07)}
.card.k-delegate{border-left-color:var(--purple)}
@keyframes rise{from{opacity:0;transform:translateY(8px)}}
.trow{display:flex;align-items:center;gap:9px;min-width:0}
.mono{flex:none;width:28px;height:28px;border-radius:8px;
  display:inline-flex;align-items:center;justify-content:center;
  font-size:14px;font-weight:700;font-family:inherit}
.trow b{font-size:15px;font-weight:600}
.meta{margin-left:auto;color:var(--secondary);font-size:12.5px;font-weight:400;white-space:nowrap}
.failed{color:var(--red);font-size:13px;font-weight:600}
.text{margin:8px 0 0;font-size:15px;color:var(--label)}
pre{margin:8px 0 0;padding:8px 10px;background:var(--code);border-radius:8px;
  font:12.5px/1.5 ui-monospace,"SF Mono",Consolas,monospace;color:rgba(60,60,67,.78);
  white-space:pre-wrap;word-break:break-word;max-height:200px;overflow:auto}
details{border-top:1px solid var(--sep);margin-top:8px;padding-top:8px}
summary{display:flex;align-items:center;justify-content:space-between;cursor:pointer;
  color:var(--blue);font-size:13px;font-weight:500;list-style:none}
summary::-webkit-details-marker{display:none}
summary::after{content:'›';color:var(--tertiary);font-size:18px;line-height:1;transition:transform .2s ease}
details[open] summary::after{transform:rotate(90deg)}
.tools{display:flex;align-items:center;gap:8px}
.btn{-webkit-appearance:none;appearance:none;border:1px solid var(--sep);background:var(--card);
  color:var(--blue);font:inherit;font-size:13px;font-weight:600;padding:5px 12px;border-radius:999px;
  cursor:pointer;white-space:nowrap;transition:background .15s ease,transform .15s ease,color .15s ease}
.btn:hover{transform:translateY(-1px)}
.btn:active{transform:translateY(0);background:var(--fill)}
.btn.off{background:var(--fill);color:var(--secondary)}
.empty{margin:24px 0;padding:28px 14px;border-radius:16px;background:var(--card);
  color:var(--secondary);font-size:14px;text-align:center}
.empty i{display:inline-block;width:7px;height:7px;margin-right:7px;border-radius:50%;
  background:var(--tertiary);animation:pulse 1.2s ease-in-out infinite;vertical-align:middle}
.step-label{margin:18px 0 2px 4px;color:var(--secondary);font-size:13px;font-weight:600;letter-spacing:.02em}
.group{margin:8px 0 8px 12px;padding-left:12px;border-left:2px solid rgba(175,82,222,.30)}
@media (prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}
</style>
</head>
<body>
<header><div class="nav">
  <h1>运行轨迹</h1>
  <div class="tools">
    <button id="toggleAll" class="btn" type="button">全部展开</button>
    <button id="autoScroll" class="btn" type="button" aria-pressed="true">自动滚动 开</button>
    <span id="status" class="pill running"><i></i>连接中</span>
  </div>
</div></header>
<main id="feed"><div id="empty" class="empty"><i></i>等待事件…</div></main>
<script>
var COLORS=['#007AFF','#34C759','#AF52DE','#FF9500','#5856D6','#30B0C7'];
var colorMap={};
function colorOf(name){
  if(!(name in colorMap)){colorMap[name]=COLORS[Object.keys(colorMap).length%COLORS.length];}
  return colorMap[name];
}
function esc(s){var d=document.createElement('div');d.textContent=(s==null?'':String(s));return d.innerHTML;}
function pretty(s){try{return JSON.stringify(JSON.parse(s),null,1);}catch(e){return s;}}
function fmtDur(ms){return ms>=1000?(ms/1000).toFixed(1)+' s':Math.round(ms)+' ms';}
function meta(ev){
  var p=[];
  if(ev.duration_ms){p.push(fmtDur(ev.duration_ms));}
  if(ev.usage&&(ev.usage.prompt_tokens||ev.usage.completion_tokens)){
    p.push((ev.usage.prompt_tokens+ev.usage.completion_tokens)+' tok');
  }
  return p.length?'<span class="meta">'+p.join(' · ')+'</span>':'';
}
function agentHead(ev,title){
  var c=colorOf(ev.agent);
  return '<div class="trow"><span class="mono" style="background:'+c+'1E;color:'+c+'">'
    +esc(ev.agent.charAt(0).toUpperCase())+'</span><b>'+title+'</b>'+meta(ev)+'</div>';
}
function render(ev){
  switch(ev.type){
    case 'run_start':
      return '<div class="card k-task">'+agentHead(ev,'任务')+'<p class="text">'+esc(ev.text)+'</p></div>';
    case 'step':
      return '<div class="step-label">Step '+ev.step+'</div>';
    case 'assistant':{
      var tc='';
      (ev.tool_calls||[]).forEach(function(c){
        tc+='<details><summary>调用 '+esc(c.name)+'</summary><pre>'+esc(pretty(c.args))+'</pre></details>';
      });
      var thought=(ev.text&&ev.text.trim())?'<pre>'+esc(ev.text)+'</pre>':'';
      return '<div class="card">'+agentHead(ev,'思考')+thought+tc+'</div>';
    }
    case 'tool_result':{
      var tool='<span class="mono" style="background:var(--fill);color:#636366">'
        +esc(ev.tool_name.charAt(0).toUpperCase())+'</span>';
      var fail=ev.is_error?'<span class="failed">失败</span>':'';
      return '<div class="card'+(ev.is_error?' k-error':'')+'"><div class="trow">'+tool+'<b>'+esc(ev.tool_name)+'</b>'+fail+meta(ev)+'</div>'
        +'<details><summary>参数</summary><pre>'+esc(pretty(ev.args))+'</pre></details>'
        +'<details open><summary>观察</summary><pre>'+esc(ev.text)+'</pre></details></div>';
    }
    case 'delegate_start':{
      var out='<span class="mono" style="background:rgba(175,82,222,.14);color:var(--purple)">↗</span>';
      return '<div class="card k-delegate"><div class="trow">'+out+'<b>委派 → '+esc(ev.tool_name)+'</b>'+meta(ev)+'</div>'
        +'<details><summary>子任务</summary><pre>'+esc(pretty(ev.args))+'</pre></details></div>';
    }
    case 'delegate_end':{
      var back='<span class="mono" style="background:rgba(175,82,222,.14);color:var(--purple)">↩</span>';
      return '<div class="card k-delegate'+(ev.is_error?' k-error':'')+'"><div class="trow">'+back+'<b>委派返回</b>'
        +(ev.is_error?'<span class="failed">失败</span>':'')+meta(ev)+'</div>'
        +'<details open><summary>报告</summary><pre>'+esc(ev.text)+'</pre></details></div>';
    }
    case 'final':
      return '<div class="card k-final">'+agentHead(ev,'最终答案')+'<p class="text">'+esc(ev.text)+'</p></div>';
    case 'error':
      return '<div class="card k-error"><div class="trow">'
        +'<span class="mono" style="background:rgba(255,59,48,.14);color:var(--red)">!</span>'
        +'<b class="failed">错误</b>'+meta(ev)+'</div><pre>'+esc(ev.text)+'</pre></div>';
  }
  return '';
}
var feed=document.getElementById('feed');
var emptyEl=document.getElementById('empty');
var stack=[]; // open delegation groups; children render inside the top group
function parent(){return stack.length?stack[stack.length-1]:feed;}
var seen=0;
var autoScrollOn=true;
var allOpen=false;
function hideEmpty(){if(emptyEl){emptyEl.style.display='none';}}
function setDetails(open){
  document.querySelectorAll('details').forEach(function(d){d.open=open;});
  var btn=document.getElementById('toggleAll');
  if(btn){btn.textContent=open?'全部折叠':'全部展开';}
  allOpen=open;
}
document.getElementById('toggleAll').addEventListener('click',function(){setDetails(!allOpen);});
document.getElementById('autoScroll').addEventListener('click',function(){
  autoScrollOn=!autoScrollOn;
  var btn=document.getElementById('autoScroll');
  btn.textContent=autoScrollOn?'自动滚动 开':'自动滚动 关';
  btn.setAttribute('aria-pressed',autoScrollOn?'true':'false');
  btn.classList.toggle('off',!autoScrollOn);
});
function poll(){
  fetch('/api/trace').then(function(r){return r.json();}).then(function(d){
    for(var i=seen;i<d.events.length;i++){
      var ev=d.events[i];
      hideEmpty();
      if(ev.type==='delegate_start'){
        parent().insertAdjacentHTML('beforeend',render(ev));
        var g=document.createElement('div');g.className='group';
        parent().appendChild(g);stack.push(g);
      }else if(ev.type==='delegate_end'){
        parent().insertAdjacentHTML('beforeend',render(ev));
        if(stack.length){stack.pop();}
      }else{
        parent().insertAdjacentHTML('beforeend',render(ev));
      }
    }
    if(d.events.length>seen && autoScrollOn){
      window.scrollTo({top:document.body.scrollHeight,behavior:'smooth'});
    }
    seen=d.events.length;
    var status=document.getElementById('status');
    if(d.done){
      status.className='pill done';
      status.textContent='✓ 已完成 · '+seen+' 个事件';
    }else{
      status.className='pill running';
      status.innerHTML='<i></i>运行中 · '+seen+' 个事件';
      setTimeout(poll,800);
    }
  }).catch(function(){setTimeout(poll,1500);});
}
poll();
</script>
</body>
</html>`

/* Neural Hive chat page: persistent conversations with full user/assistant history (application store), algorithm simulation, cost estimate, paid multi-agent run via MetaMask. */
(function(){
  var N=window.NH; var API=N.API; var walletState=N.wallet; var PAY=null;
  var running=false;
  function D(id){ return document.getElementById(id); }
  function el(tag,cls,text){ var e=document.createElement(tag); if(cls){ e.className=cls; } if(text!=null){ e.textContent=text; } return e; }
  function fmtElapsed(start){ return Math.max(0,Math.floor((Date.now()-start)/1000))+"s"; }
  function tickTimers(){ var n=document.querySelectorAll(".nh-elapsed"); for(var i=0;i<n.length;i++){ n[i].textContent=fmtElapsed(parseInt(n[i].getAttribute("data-start"),10)); } }
  function notify(msg,bad,kind){ return N.notify(msg,bad,kind); }
  function algo(){ var m=D("krumM"); return { routing:D("routing").value, aggregation:D("aggregation").value, byzantine:parseInt(D("byzantine").value,10)||0, krumM:(m?parseInt(m.value,10):0)||0, simulate:true }; }

  // buildHistory returns the last HUB_HISTORY_LIMIT turns of the active thread (user/assistant) so the
  // agents answer with the same conversation context the user sees. The limit is server-configured.
  // currentHistoryLimit reads the user chosen cap from the composer control (-1 = entire conversation,
  // 0/absent = use the server default HUB_HISTORY_LIMIT).
  function currentHistoryLimit(){ var s=D("historyLimit"); if(!s){ return 0; } var v=parseInt(s.value,10); return isNaN(v)?0:v; }
  // buildHistory returns the exact conversation the agents receive: an ordered array of
  // {role, content} turns (user/assistant) matching what the user sees, capped to the chosen
  // number of most recent messages (or the whole conversation when the cap is -1). This is what
  // lets the agents remember who the user is across turns.
  function buildHistory(t){ var limit=currentHistoryLimit(); if(limit===0){ limit=(PAY&&PAY.historyLimit)||8; } var msgs=(t&&t.messages)||[]; var turns=[]; for(var i=0;i<msgs.length;i++){ var m=msgs[i]; if(m.role==="user"&&m.text){ turns.push({role:"user",content:m.text}); } else if(m.role==="assistant"&&m.text&&m.status!=="error"&&m.status!=="running"){ turns.push({role:"assistant",content:m.text}); } } if(limit>0&&turns.length>limit){ turns=turns.slice(turns.length-limit); } return turns; }
  // toMetaData returns a PLAIN serializable object (no DOM nodes), so the assistant reply
  // and its execution metadata survive JSON into localStorage and can be rebuilt on refresh.
  function toMetaData(r){
    var a=r.algorithms||{}; var c=r.cost||{};
    return { complexity:(r.complexity||{}).level||null, agentsUsed:r.agentsUsed||[],
      routing:(a.routeModeUsed||a.routing||null), aggregation:a.aggregation||null,
      costHive:(c.totalHive!=null?c.totalHive:null), answerHash:r.answerHash||null,
      anchor:r.anchor||null, payment:r.payment||null, taskId:r.taskId||null, aggregatorNote:a.aggregatorNote||null, krumKeep:a.krumKeep||null, krumOutliers:a.krumOutliers||null };
  }
  // renderMeta rebuilds the visible execution-details block from the persisted plain object.
  function renderMeta(md){
    if(!md||typeof md!=="object"){ return null; }
    var box=el("div","nh-muted"); box.style.marginTop="12px"; box.style.borderTop="1px solid var(--line2)"; box.style.paddingTop="10px";
    function line(parts){ var d=el("div"); for(var i=0;i<parts.length;i++){ d.appendChild(parts[i]); } box.appendChild(d); return d; }
    function b(t){ return el("b",null,t); } function tx(s){ return document.createTextNode(s); }
    line([b("Complexity "),tx(md.complexity||"-"),tx("  -  Agents "),tx((md.agentsUsed||[]).join(", "))]);
    line([b("Routing "),tx(md.routing||"selfish"),tx("  -  Aggregation "),tx(md.aggregation||"-"),tx("  -  Cost "),tx(md.costHive!=null?md.costHive:"-"),tx(" HIVE")]);
    if(md.aggregatorNote){ line([b("Aggregation note "),tx(md.aggregatorNote)]); }
    if(md.answerHash){ line([b("Answer hash "),tx(md.answerHash)]); }
    if(md.anchor){ line([b("On-chain anchor "),tx(md.anchor.ok?"yes":"no"),tx(" "),tx(md.anchor.requestTx||"")]); }
    if(md.payment){ line([b("Payment "),tx((md.payment.paidHive||"0")+" HIVE "),tx(md.payment.mode||""),tx(" "),tx(md.payment.txHash||"")]); }
    if(md.taskId){ var a2=el("a","btn","Open Execution Details"); a2.href="/execution.html?id="+encodeURIComponent(md.taskId); a2.target="_blank"; a2.style.marginTop="8px"; box.appendChild(a2); }
    return box; }
  function doEstimate(){ var q=D("input").value.trim(); if(!q){ return; } var th=N.threads.active(); N.post("/estimate", Object.assign({task:q, history:buildHistory(th), historyLimit:currentHistoryLimit()}, algo())).then(function(d){ if(!d.__ok||d.error){ throw new Error(d.error||("HTTP "+d.__status)); } var c=d.cost||{}; notify("Estimated cost: "+c.totalHive+" HIVE for "+(c.subtasks||0)+" subtask(s)",false); }).catch(function(e){ notify(N.errText(e),true); }); }
  function topUp(q,setPhase){ if(!PAY||!PAY.faucet){ return Promise.resolve(); } var need=BigInt(q.costWei); var low=(walletState.hiveWei==null)||(walletState.hiveWei<need); if(!low){ return Promise.resolve(); } setPhase("Wallet short of HIVE - requesting test funds from the devnet faucet"); return N.faucet(walletState.account).then(function(d){ if(!d.__ok||d.ok===false){ throw new Error("Faucet failed: "+(d.error||d.__status)); } return N.refreshBalance(); }); }

  function msgNode(m){ var wrap=el("div","nh-msg"); wrap.setAttribute("data-id",m.id); if(m.role==="user"){ wrap.appendChild(el("div","bubble-user",N.str(m.text))); } else { var ans=el("div","answer"); var body=el("div","ans-body"); if(m.status==="running"){ var th2=el("div","nh-thinking"); th2.appendChild(el("span","nh-spin")); th2.appendChild(el("span",null,m.phase||"Working...")); var tm=el("span","nh-elapsed"); tm.setAttribute("data-start",m.startedAt||Date.now()); tm.textContent=fmtElapsed(m.startedAt||Date.now()); th2.appendChild(tm); body.appendChild(th2); } else { var prose=el("div","nh-prose"); prose.innerHTML=N.md(N.str(m.text)); body.appendChild(prose); var metaNode=(m.metaData!=null)?renderMeta(m.metaData):((m.meta&&m.meta.nodeType===1)?m.meta:null); if(metaNode){ body.appendChild(metaNode); } } ans.appendChild(body); wrap.appendChild(ans); } return wrap; }
  function renderThread(){ var th=D("thread"); if(!th){ return; } var t=N.threads.active(); th.textContent=""; if(!t||!t.messages.length){ var em=el("div","empty"); em.appendChild(el("div","big","Ask the Hive")); em.appendChild(el("div",null,"A request is routed to independent specialised agents, aggregated into one verified answer, and paid in HIVE from your wallet.")); th.appendChild(em); return; } for(var i=0;i<t.messages.length;i++){ th.appendChild(msgNode(t.messages[i])); } var cw=document.querySelector(".chat-wrap"); if(cw){ cw.scrollTop=cw.scrollHeight; } }
  function renderThreads(){ var box=D("threadList"); if(!box){ return; } var list=N.threads.list(); var act=N.store.getState().activeThreadId; box.textContent=""; for(var i=0;i<list.length;i++){ var t=list[i]; var row=el("div","nh-thread"+(t.id===act?" active":"")); row.setAttribute("data-id",t.id); row.appendChild(el("span","t",t.title)); box.appendChild(row); } }

  function submit(){ if(running){ return; } var inp=D("input"); var q=inp.value.trim(); if(!q){ return; } notify(""); running=true; D("send").disabled=true; inp.value=""; inp.style.height="auto";
    var t=N.threads.active(); var history=buildHistory(t); var historyLimit=currentHistoryLimit(); N.threads.addMessage(t.id,{role:"user",text:q}); var asst=N.threads.addMessage(t.id,{role:"assistant",status:"running",phase:"Connecting MetaMask...",startedAt:Date.now()}); renderThreads(); renderThread();
    function setPhase(phase){ N.threads.updateMessage(t.id,asst.id,{phase:phase,status:"running"}); renderThread(); }
    setPhase("Connecting MetaMask..."); var quote=null;
    N.connect().then(function(){ walletRender(); setPhase("Pricing the request..."); return N.post("/quote", Object.assign({task:q, history:history, historyLimit:historyLimit}, algo())); })
      .then(function(d){ if(!d.__ok||d.error){ throw new Error(d.error||("HTTP "+d.__status)); } quote=d; return topUp(d,setPhase); })
      .then(function(){ setPhase("Confirm the "+quote.costHive+" HIVE payment in MetaMask"); return N.transferHive(quote.treasury, quote.costWei); })
      .then(function(tx){ setPhase("Payment confirmed - verifying on chain, routing agents (this can take a moment)"); return N.post("/task", Object.assign({task:q, quoteId:quote.quoteId, hive:quote.hive, txHash:tx, history:history, historyLimit:historyLimit}, algo())); })
      .then(function(d){ if(!d.__ok||d.error){ throw new Error(d.error||("HTTP "+d.__status)); } N.threads.updateMessage(t.id,asst.id,{status:"done",text:N.str(d.answer),metaData:toMetaData(d)}); renderThread(); })
      .catch(function(e){ N.threads.updateMessage(t.id,asst.id,{status:"error",text:N.errText(e)}); renderThread(); notify(N.errText(e),true); })
      .then(function(){ running=false; D("send").disabled=false; N.refreshBalance().then(walletRender).catch(function(){}); });
  }
  var walletMount=null; function walletRender(){ if(walletMount){ walletMount.render(); } }

  function faucet(){ notify(""); N.connect().then(function(){ walletRender(); return N.faucet(walletState.account); }).then(function(d){ if(!d.__ok||d.ok===false){ throw new Error("Faucet failed: "+(d.error||d.__status)); } return N.refreshBalance(); }).then(function(){ walletRender(); notify("Faucet topped your wallet up with test HIVE (devnet).",false,"ok"); }).catch(function(e){ notify(N.errText(e),true); }); }
  function loadAgents(){ N.get("/agents").then(function(d){ var list=(d.agents||[]); var box=D("agentList"); if(!box){ return; } box.textContent=""; if(!list.length){ box.appendChild(el("div","nh-muted","No agents yet.")); return; } var t=el("table","nh-minitable"); var hr=el("tr"); ["Agent","Status","Balance"].forEach(function(h){ hr.appendChild(el("th",null,h)); }); var thd=el("thead"); thd.appendChild(hr); t.appendChild(thd); var tb=el("tbody"); var n=Math.min(list.length,8); for(var i=0;i<n;i++){ var a=list[i]; var tr=el("tr"); tr.appendChild(el("td",null,(a.name||a.id))); var sd=el("td"); sd.appendChild(el("span","dot "+(a.status==="online"?"ok":"bad"))); sd.appendChild(document.createTextNode(" "+(a.status||"offline"))); tr.appendChild(sd); var bd=el("td"); bd.appendChild(el("span","nh-bal",((a.balanceHive!=null?a.balanceHive:"0")+" HIVE"))); tr.appendChild(bd); tb.appendChild(tr); } t.appendChild(tb); box.appendChild(t); if(list.length>n){ box.appendChild(el("div","nh-muted",(list.length-n)+" more agents")); } }).catch(function(){}); }
  function loadNetwork(){ N.get("/network").then(function(d){ var e=D("net"); if(e){ e.textContent="agents "+d.online+"/"+d.agents+" online - tasks "+d.completedTasks+" - "+(d.payment?d.payment.pricePerAgent:"-")+" HIVE/agent"; } if(D("maxAgents")){ applyMaxAgentsView({ maxAgents:(d.hive&&d.hive.maxAgents!=null)?d.hive.maxAgents:null, maxAgentsCeiling:d.hive?d.hive.max:null, online:d.online }); } }).catch(function(){}); }
  function loadPay(){ return N.loadPay().then(function(c){ PAY=c; var ch=D("costHint"); if(ch){ ch.innerHTML=""; ch.appendChild(el("b",null,c.pricePerAgent)); ch.appendChild(document.createTextNode(" HIVE per agent - a simple request uses 1 agent, a complex one more.")); } applyMaxAgentsView(c); return c; }); }
  function applyMaxAgentsView(c){ if(!c){ return; } var inp=D("maxAgents"); var hint=D("maxAgentsHint"); var ceil=(c.maxAgentsCeiling!=null?c.maxAgentsCeiling:(c.hive&&c.hive.max)||null); if(inp){ if(ceil!=null){ inp.max=String(ceil); } var cur=(c.maxAgents!=null?c.maxAgents:null); if(cur!=null&&!inp.dataset.touched){ inp.value=String(cur); } } if(hint){ hint.textContent="ceiling "+(ceil!=null?ceil:"-")+" - online "+((c.online!=null)?c.online:"-"); } }
  function saveMaxAgents(){ var inp=D("maxAgents"); if(!inp){ return; } var v=parseInt(inp.value,10); if(isNaN(v)||v<1){ v=1; } notify(""); N.post("/agents/limit",{max:v}).then(function(d){ if(!d.__ok||d.error){ throw new Error(d.error||("HTTP "+d.__status)); } inp.dataset.touched=""; applyMaxAgentsView(d); N.store.setPrefs({ maxAgents:parseInt(inp.value,10)||0 }); notify("Max agents per request set to "+d.maxAgents+".",false,"ok"); loadNetwork(); }).catch(function(e){ notify(N.errText(e),true); }); }

  function bindSidebar(){ var nb=D("newChat"); if(nb){ nb.onclick=function(){ N.threads.new("New chat"); renderThreads(); renderThread(); D("input").focus(); }; } var tl=D("threadList"); if(tl){ tl.addEventListener("click", function(ev){ var del=ev.target.getAttribute("data-del"); if(del){ ev.stopPropagation(); N.threads.delete(del); renderThreads(); renderThread(); return; } var node=ev.target.closest(".nh-thread"); if(node){ N.threads.setActive(node.getAttribute("data-id")); renderThreads(); renderThread(); } }); } }
  function init(){
    D("send").onclick=submit; D("estimateBtn").onclick=doEstimate; var fb=D("faucetBtn"); if(fb){ fb.onclick=faucet; } var ma=D("maxAgents"); var ms=D("maxAgentsSave"); if(ms){ ms.onclick=saveMaxAgents; } if(ma){ ma.addEventListener("input",function(){ ma.dataset.touched="1"; }); ma.addEventListener("change",saveMaxAgents); }
    walletMount=N.mountWallet("walletBtn");
    bindSidebar();
    var prefs=N.store.getState().prefs||{}; if(D("routing")&&prefs.routing){ D("routing").value=prefs.routing; } if(D("aggregation")&&prefs.aggregation){ D("aggregation").value=prefs.aggregation; } if(D("byzantine")&&prefs.byzantine!=null){ D("byzantine").value=prefs.byzantine; } if(D("krumM")&&prefs.krumM!=null){ D("krumM").value=String(prefs.krumM); } if(D("historyLimit")&&prefs.historyLimit!=null){ D("historyLimit").value=String(prefs.historyLimit); } if(D("maxAgents")&&prefs.maxAgents){ D("maxAgents").value=String(prefs.maxAgents); D("maxAgents").dataset.touched="1"; }
    function savePrefs(){ N.store.setPrefs({ routing:D("routing").value, aggregation:D("aggregation").value, byzantine:parseInt(D("byzantine").value,10)||0, krumM:parseInt((D("krumM")||{}).value,10)||0, historyLimit:currentHistoryLimit() }); }
    D("routing").onchange=savePrefs; D("aggregation").onchange=savePrefs; D("byzantine").onchange=savePrefs; if(D("krumM")){ D("krumM").onchange=savePrefs; } if(D("historyLimit")){ D("historyLimit").onchange=savePrefs; }
    var inp=D("input"); inp.addEventListener("input",function(){ inp.style.height="auto"; inp.style.height=Math.min(inp.scrollHeight,170)+"px"; });
    inp.addEventListener("keydown",function(e){ if(e.key==="Enter" && !e.shiftKey){ e.preventDefault(); submit(); } });
    N.store.subscribe(function(){ renderThreads(); });
    loadPay().catch(function(){}); loadAgents(); loadNetwork(); renderThreads(); renderThread();
    setInterval(tickTimers,1000); setInterval(loadAgents,12000); setInterval(loadNetwork,12000);
    notify("Welcome to Neural Hive. Connect MetaMask, add agents, and run a verified multi-agent request.",false,"info");
  }
  if(document.readyState==="loading"){ document.addEventListener("DOMContentLoaded",init); } else { init(); }
})();


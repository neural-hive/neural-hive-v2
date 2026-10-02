// Neural Hive shared frontend library: API, persistent application store, markdown,
// formatting, wallet (MetaMask) and the live event stream.
window.NH = (function () {
  var API = (location.port === '9500') ? '' : 'http://127.0.0.1:9500';
  var ZERO = BigInt(0), UNIT = BigInt('1000000000000000000');
  var STORE_KEY = 'nh.store.v1';
  function defaultState(){ return { threads: [], activeThreadId: null, prefs: { routing: 'selfish', aggregation: 'mean', byzantine: 0 }, wallet: { account: null } }; }
  function readState(){ try { var raw = localStorage.getItem(STORE_KEY); if(!raw){ return defaultState(); } var s = JSON.parse(raw); if(!s || typeof s !== 'object'){ return defaultState(); } if(!s.threads){ s.threads = []; } if(!s.prefs){ s.prefs = {}; } if(!s.wallet){ s.wallet = { account: null }; } return s; } catch(e){ return defaultState(); } }
  var state = readState();
  var subs = [];
  function persist(){ try { localStorage.setItem(STORE_KEY, JSON.stringify(state)); } catch(e){} }
  function emit(){ for(var i=0;i<subs.length;i++){ try { subs[i](state); } catch(e){} } }
  var store = {
    getState: function(){ return state; },
    subscribe: function(fn){ subs.push(fn); return function(){ var i=subs.indexOf(fn); if(i>=0){ subs.splice(i,1); } }; },
    patch: function(p){ Object.assign(state, p); persist(); emit(); },
    setPrefs: function(p){ Object.assign(state.prefs, p); persist(); emit(); },
    setWallet: function(w){ Object.assign(state.wallet, w); persist(); emit(); },
    reset: function(){ state = defaultState(); persist(); emit(); }
  };
  function uid(prefix){ return (prefix||'t')+'-'+Date.now().toString(36)+'-'+Math.random().toString(36).slice(2,8); }
  function listThreads(){ return state.threads.slice().sort(function(a,b){ return (b.updatedAt||0)-(a.updatedAt||0); }); }
  function getThread(id){ for(var i=0;i<state.threads.length;i++){ if(state.threads[i].id===id){ return state.threads[i]; } } return null; }
  function newThread(title){ var t = { id: uid('thread'), title: title||'New chat', createdAt: Date.now(), updatedAt: Date.now(), messages: [] }; state.threads.push(t); state.activeThreadId = t.id; persist(); emit(); return t; }
  function activeThread(){ var t = state.activeThreadId ? getThread(state.activeThreadId) : null; if(!t){ t = listThreads()[0] || newThread('New chat'); state.activeThreadId = t.id; persist(); emit(); } return t; }
  function addMessage(threadId, msg){ var t = getThread(threadId); if(!t){ return null; } var m = Object.assign({ id: uid('m'), at: Date.now() }, msg); t.messages.push(m); t.updatedAt = Date.now(); if((!t.title || t.title==='New chat') && msg.role==='user' && msg.text){ t.title = String(msg.text).slice(0,48); } persist(); emit(); return m; }
  function updateMessage(threadId, msgId, patch){ var t = getThread(threadId); if(!t){ return null; } for(var i=0;i<t.messages.length;i++){ if(t.messages[i].id===msgId){ Object.assign(t.messages[i], patch); t.updatedAt = Date.now(); persist(); emit(); return t.messages[i]; } } return null; }
  function deleteThread(id){ state.threads = state.threads.filter(function(t){ return t.id!==id; }); if(state.activeThreadId===id){ state.activeThreadId = state.threads.length?listThreads()[0].id:null; } persist(); emit(); }
  function setActiveThread(id){ state.activeThreadId = id; persist(); emit(); }

  
  function str(v){ if(v==null){ return ''; } return (typeof v==='string')?v:String(v); }
  // safeStartsWith guards against non-string values (e.g. persisted state) reaching String.prototype.startsWith.
  function safeStartsWith(v, prefix){ try { return str(v).startsWith(prefix); } catch(e){ return false; } }
  function esc(s){ var A=String.fromCharCode(38); var t=(s==null)?'':String(s); return t.split(A).join(A+'amp;').replace(/</g,A+'lt').replace(/>/g,A+'gt'); }
  function qs(name){ var m=new RegExp('[?&]'+name+'=([^&]*)').exec(location.search); return m?decodeURIComponent(m[1].replace(/\+/g,' ')):null; }
  function fmt(x,n){ if(typeof x!=='number'){ return (x==null||x==='')?'-':x; } return x.toFixed(n==null?4:n); }
  function shortId(x){ if(!x||typeof x!=='string'){ return '-'; } return esc(x.length<=30?x:x.slice(0,16)+'...'+x.slice(-12)); }
  function shortAddr(a){ return (a&&a.length>12)?a.slice(0,6)+'...'+a.slice(-4):(a||''); }
  function hiveStr(wei){ var b=BigInt(wei); var w=b/UNIT; var r=b%UNIT; if(r===ZERO){ return w.toString(); } var f=r.toString(); while(f.length<18){ f='0'+f; } f=f.replace(/0+$/,''); if(f.length>4){ f=f.slice(0,4).replace(/0+$/,''); } if(!f&&w===ZERO){ return '<0.0001'; } return w.toString()+(f?'.'+f:''); }
  function inline(t){ var parts=String(t==null?'':t).split(/<br\s*\/?>/i); var out=[]; for(var i=0;i<parts.length;i++){ out.push(inlineOne(parts[i])); } return out.join('<br>'); }
  function inlineOne(t){ var s=esc(t); s=s.replace(/`([^`]+)`/g,'<code>$1</code>'); s=s.replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>'); s=s.replace(/(^|[^*\w])\*([^*\s][^*]*?)\*(?!\w)/g,'$1<em>$2</em>'); s=s.replace(/~~([^~]+)~~/g,'<del>$1</del>'); s=s.replace(/\[([^\]]+)\]\(([^)]+)\)/g,'<a href='+String.fromCharCode(34)+'$2'+String.fromCharCode(34)+' target='+String.fromCharCode(34)+'_blank'+String.fromCharCode(34)+' rel='+String.fromCharCode(34)+'noopener'+String.fromCharCode(34)+'>$1</a>'); return s; }
  function json(res){ return res.text().then(function(t){ var d={}; try{ d=t?JSON.parse(t):{}; }catch(e){ d={error:t}; } d.__status=res.status; d.__ok=res.ok; return d; }); }
  function post(path, body){ return fetch(API+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body||{})}).then(json); }
  function get(path){ return fetch(API+path).then(json); }

  // md renders markdown with marked (GFM: tables, nested lists, task lists, images, strikethrough...) and
  // sanitises the HTML with DOMPurify. Falls back to the built-in mdLite renderer if the libraries are missing.
  var mdReady=false;
  function mdInit(){ if(mdReady){ return true; } if(!window.marked||!window.DOMPurify){ return false; } try { var mk=window.marked; (mk.use||mk.marked.use).call(mk.marked||mk,{gfm:true,breaks:true}); mdReady=true; if(window.DOMPurify.addHook){ window.DOMPurify.addHook('afterSanitizeAttributes',function(node){ if(node.tagName==='A'){ node.setAttribute('target','_blank'); node.setAttribute('rel','noopener noreferrer'); } if(node.tagName==='INPUT'){ node.setAttribute('disabled',''); } }); } return true; } catch(e){ return false; } }
  function md(text){
    var src=(text==null)?'':String(text);
    if(!mdInit()){ return mdLite(src); }
    try {
      // agents sometimes join table rows with a literal <br>; turn that back into real newlines
      src=src.replace(/\r\n/g,'\n').replace(/\|[ \t]*<br\s*\/?>[ \t]*(?=\|)/gi,'|\n');
      var parse=(window.marked.parse||window.marked.marked&&window.marked.marked.parse||window.marked);
      var html=parse(src);
      return window.DOMPurify.sanitize(html,{ADD_ATTR:['target']});
    } catch(e){ return mdLite(src); }
  }
  function mdLite(text){
    var src=(text==null)?'':String(text); var NL=String.fromCharCode(10); src=src.replace(/\r\n/g, NL).replace(/\|[ \t]*<br\s*\/?>[ \t]*(?=\|)/gi, '|'+NL); var lines=src.split(NL); var out=[]; var i=0;
    function isSep(l){ return l!=null && l.indexOf('|')>=0 && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(l); }
    function tableAt(k){ return k+1<lines.length && lines[k].indexOf('|')>=0 && isSep(lines[k+1]); }
    function cells(l){ l=l.trim(); if(l.charAt(0)==='|'){ l=l.slice(1); } if(l.charAt(l.length-1)==='|'){ l=l.slice(0,-1); } return l.split('|').map(function(c){ return c.trim(); }); }
    function isB(l){return /^\s*[-*]\s+/.test(l);} function isN(l){return /^\s*\d+\.\s+/.test(l);} function isH(l){return /^#{1,6}\s+/.test(l);} function isF(l){return /^```/.test(l);} function isR(l){return /^\s*(-{3,}|\*{3,})\s*$/.test(l);}
    while(i<lines.length){
      var ln=lines[i];
      if(isF(ln)){ i++; var buf=[]; while(i<lines.length&&!isF(lines[i])){ buf.push(lines[i]); i++; } i++; out.push('<pre><code>'+esc(buf.join(NL))+'</code></pre>'); continue; }
      var m=ln.match(/^(#{1,6})\s+(.*)$/); if(m){ var lv=Math.min(m[1].length,6); out.push('<h'+lv+'>'+inline(m[2])+'</h'+lv+'>'); i++; continue; }
      if(isR(ln)){ out.push('<hr>'); i++; continue; }
      if(/^>\s?/.test(ln)){ var q=[]; while(i<lines.length&&/^>\s?/.test(lines[i])){ q.push(lines[i].replace(/^>\s?/,'')); i++; } out.push('<blockquote>'+inline(q.join('<br>'))+'</blockquote>'); continue; }
      if(tableAt(i)){ var hd=cells(lines[i]); var al=cells(lines[i+1]).map(function(c){ var l=c.charAt(0)===':', r=c.charAt(c.length-1)===':'; return (l&&r)?'center':(r?'right':(l?'left':'')); }); i+=2; var th='<tr>'; for(var h=0;h<hd.length;h++){ th+='<th'+(al[h]?' style="text-align:'+al[h]+'"':'')+'>'+inline(hd[h])+'</th>'; } th+='</tr>'; var tb=[]; while(i<lines.length&&lines[i].trim()!==''&&lines[i].indexOf('|')>=0){ var cs=cells(lines[i]); var tr='<tr>'; for(var c=0;c<hd.length;c++){ tr+='<td'+(al[c]?' style="text-align:'+al[c]+'"':'')+'>'+inline(cs[c]!=null?cs[c]:'')+'</td>'; } tb.push(tr+'</tr>'); i++; } out.push('<div class="md-table-wrap"><table class="md-table"><thead>'+th+'</thead><tbody>'+tb.join('')+'</tbody></table></div>'); continue; }
      if(isB(ln)){ var li=[]; while(i<lines.length&&isB(lines[i])){ li.push('<li>'+inline(lines[i].replace(/^\s*[-*]\s+/,''))+'</li>'); i++; } out.push('<ul>'+li.join('')+'</ul>'); continue; }
      if(isN(ln)){ var lo=[]; while(i<lines.length&&isN(lines[i])){ lo.push('<li>'+inline(lines[i].replace(/^\s*\d+\.\s+/,''))+'</li>'); i++; } out.push('<ol>'+lo.join('')+'</ol>'); continue; }
      if(ln.trim()===''){ i++; continue; }
      var para=[]; while(i<lines.length&&lines[i].trim()!==''&&!isH(lines[i])&&!isF(lines[i])&&!isB(lines[i])&&!isN(lines[i])&&!isR(lines[i])&&!tableAt(i)&&!/^>\s?/.test(lines[i])){ para.push(lines[i]); i++; }
      out.push('<p>'+inline(para.join('<br>'))+'</p>');
    }
    return out.join('');
  }


  // notify shows a side toast (top-right). kind: 'warn' (default), 'ok', 'info', 'bad'. Empty msg clears all toasts.
  var toastBox=null;
  function toastHost(){ if(toastBox&&document.body.contains(toastBox)){ return toastBox; } toastBox=document.createElement('div'); toastBox.className='nh-toasts'; toastBox.setAttribute('aria-live','polite'); document.body.appendChild(toastBox); return toastBox; }
  function dismissToast(t){ if(!t||t._gone){ return; } t._gone=true; clearTimeout(t._timer); t.classList.add('out'); setTimeout(function(){ if(t.parentNode){ t.parentNode.removeChild(t); } },300); }
  function notify(msg, bad, kind){
    var host=toastHost();
    if(!msg){ var all=host.querySelectorAll('.nh-toast'); for(var i=0;i<all.length;i++){ dismissToast(all[i]); } return null; }
    msg=String(msg); var k=bad?'bad':(kind||'warn');
    var ex=host.querySelectorAll('.nh-toast'); for(var j=0;j<ex.length;j++){ if(ex[j]._msg===msg){ dismissToast(ex[j]); } }
    while(host.querySelectorAll('.nh-toast:not(.out)').length>=4){ dismissToast(host.querySelector('.nh-toast:not(.out)')); }
    var icons={warn:'!',ok:'\u2713',info:'i',bad:'\u00d7'}; var titles={warn:'Notice',ok:'Success',info:'Neural Hive',bad:'Error'};
    var t=document.createElement('div'); t.className='nh-toast '+k; t._msg=msg; t.setAttribute('role',bad?'alert':'status');
    var ic=document.createElement('div'); ic.className='ti'; ic.textContent=icons[k]||'!';
    var bd=document.createElement('div'); bd.className='tb'; var tt=document.createElement('div'); tt.className='tt'; tt.textContent=titles[k]||'Notice'; var tm=document.createElement('div'); tm.className='tm'; tm.textContent=msg; bd.appendChild(tt); bd.appendChild(tm);
    var x=document.createElement('button'); x.className='tx'; x.type='button'; x.setAttribute('aria-label','Dismiss'); x.textContent='\u00d7'; x.onclick=function(){ dismissToast(t); };
    var dur=bad?10000:Math.min(9000,4500+msg.length*30);
    var pr=document.createElement('div'); pr.className='tp'; var bar=document.createElement('i'); bar.style.setProperty('--dur',dur+'ms'); pr.appendChild(bar);
    t.appendChild(ic); t.appendChild(bd); t.appendChild(x); t.appendChild(pr); host.appendChild(t);
    bar.addEventListener('animationend',function(){ dismissToast(t); });
    return t;
  }

  var walletState = { account:null, hiveWei:null, ethWei:null };
  var PAY = null;
  function eth(){ return window.ethereum || null; }
  function rpc(method, params){ if(!eth()){ return Promise.reject(new Error('MetaMask not found')); } return eth().request({ method: method, params: params || [] }); }
  function acct(a){ return addrStr(a); }
  function pad32(hex){ var h=String(addrStr(hex)).replace(/^0x/,'').toLowerCase(); while(h.length<64){ h='0'+h; } return h; }
  function encTransfer(to, amountWei){ return '0xa9059cbb'+pad32(to)+pad32(BigInt(amountWei).toString(16)); }
  function encBalanceOf(addr){ return '0x70a08231'+pad32(addr); }
  function toHexId(v){ if(v==null||v===''){ return null; } if(typeof v==='number'){ return '0x'+v.toString(16); } var s=String(v).trim(); if(/^0x[0-9a-fA-F]+$/.test(s)){ return s.toLowerCase(); } if(/^[0-9]+$/.test(s)){ return '0x'+Number(s).toString(16); } return null; }
  function addrStr(v){ if(v==null){ return v; } if(typeof v==='string'){ return v.trim(); } if(typeof v==='object'){ return String(v.address||v.addr||v.id||''); } return String(v); }
  function normPay(c){ if(!c||typeof c!=='object'){ return c; } var id=toHexId(c.chainIdHex)||toHexId(c.chainId)||'0x539'; c.chainIdHex=id; c.chainId=parseInt(id,16); if(c.token!=null){ c.token=addrStr(c.token); } if(c.treasury!=null){ c.treasury=addrStr(c.treasury); } if(c.rpcUrl!=null){ c.rpcUrl=String(c.rpcUrl); } return c; }
  function loadPay(){ if(PAY){ return Promise.resolve(PAY); } return get('/payment/config').then(function(c){ if(c && !c.error){ PAY=normPay(c); } return c; }); }
  function ensureChain(){
    if(!PAY){ return Promise.reject(new Error('payment config not loaded')); }
    var want=toHexId(PAY.chainIdHex)||toHexId(PAY.chainId)||'0x539';
    return rpc('eth_chainId').then(function(cur){
      if(String(cur).toLowerCase()===String(want).toLowerCase()){ return; }
      return rpc('wallet_switchEthereumChain',[{chainId:want}]).catch(function(e){
        var missing = e && (e.code===4902 || /unrecognized chain|not been added|unknown chain/i.test(e.message||''));
        if(!missing){ throw e; }
        return rpc('wallet_addEthereumChain',[{chainId:want,chainName:'Neural Hive Devnet',nativeCurrency:{name:'Ether',symbol:'ETH',decimals:18},rpcUrls:[PAY.rpcUrl||'http://127.0.0.1:8545']}]);
      });
    });
  }
  function refreshBalance(){
    walletState.account = acct(walletState.account || (state.wallet && state.wallet.account)) || null;
    if(!walletState.account || !PAY || !PAY.token){ return Promise.resolve(walletState); }
    return rpc('eth_call',[{to:PAY.token,data:encBalanceOf(walletState.account)},'latest']).then(function(hex){
      walletState.hiveWei=BigInt(hex&&hex!=='0x'?hex:'0x0');
      return rpc('eth_getBalance',[walletState.account,'latest']).catch(function(){ return '0x0'; });
    }).then(function(hex){ walletState.ethWei=BigInt(hex||'0x0'); return walletState; });
  }
  function connect(){
    if(!eth()){ return Promise.reject(new Error('MetaMask not found. Install the extension from https://metamask.io')); }
    return loadPay().then(function(){ return rpc('eth_requestAccounts'); }).then(function(accs){
      if(!accs||!accs.length){ throw new Error('No MetaMask account selected.'); }
      walletState.account=acct(accs[0]); store.setWallet({ account: walletState.account });
      return ensureChain();
    }).then(function(){ return refreshBalance(); });
  }
  function waitReceipt(hash){ var t0=Date.now(); function poll(){ return rpc('eth_getTransactionReceipt',[hash]).then(function(rc){ if(rc){ if(rc.status==='0x0'){ throw new Error('transaction reverted on chain'); } return rc; } if(Date.now()-t0>120000){ throw new Error('transaction not confirmed within 2 minutes'); } return new Promise(function(res){ setTimeout(res,1200); }).then(poll); }); } return poll(); }
  function faucet(address){ return post('/faucet',{address:address}); }
  function encUpdatePrice(priceWei){ return '0x8d6cc56d'+pad32(BigInt(priceWei).toString(16)); }
  // isNonceErr detects the MetaMask/RPC stale-nonce rejection ("the tx does not have the correct
  // nonce" / nonce too low). Several Neural Hive processes share the deployer key on the devnet, so
  // the wallet local nonce cache can lag behind the chain; callers must retry with a fresh nonce.
  function isNonceErr(e){ var m=(e&&(e.message||e.data&&e.data.message))||""; m=String(m).toLowerCase(); return m.indexOf("nonce")>=0; }
  // nextNonce fetches the account pending nonce straight from the node so a stale wallet cache can
  // never make us submit a transaction the account has already consumed.
  function nextNonce(account){ return rpc("eth_getTransactionCount",[account,"pending"]).then(function(h){ return h; }); }
  // sendTransaction builds and sends a contract transaction with an explicit, freshly read nonce and
  // transparently retries once if the node still reports a nonce mismatch (self-healing).
  function sendTransaction(from,to,data,valueHex){
    from=acct(from); to=acct(to);
    if(!from){ return Promise.reject(new Error('Wallet not connected.')); }
    if(!to){ return Promise.reject(new Error('Missing contract/treasury address from server config.')); }
    function attempt(n){
      var tx={ from:from, to:to, value:String(valueHex||'0x0'), data:String(data) };
      if(n!=null){ tx.nonce=String(n); }
      return rpc('eth_sendTransaction',[tx]).catch(function(e){
        if(n==null && isNonceErr(e)){ return nextNonce(from).then(function(fresh){ return attempt(fresh); }); }
        throw e;
      });
    }
    return attempt(null);
  }
  // updateAgentPrice sends CapabilityRegistry.updatePrice(priceWei) from the connected owner wallet.
  function updateAgentPrice(registry, priceWei){ return sendTransaction(walletState.account,registry,encUpdatePrice(priceWei),'0x0').then(function(hash){ return waitReceipt(hash).then(function(){ return hash; }); }); }
  // sendTx sends a raw contract call (to, data) from the connected account and resolves with the tx hash after the receipt. Used for owner-only calls such as AgentWallet.withdraw().
  function sendTx(to, data){ return sendTransaction(walletState.account,to,data,'0x0').then(function(hash){ return waitReceipt(hash).then(function(){ return hash; }); }); }
  function transferHive(to, costWei){ if(!PAY||!PAY.token){ return Promise.reject(new Error('Payment config not loaded.')); } to=addrStr(to); if(!to){ return Promise.reject(new Error('Server quote has no treasury address.')); } return sendTransaction(walletState.account,PAY.token,encTransfer(to,costWei),'0x0').then(function(hash){ return waitReceipt(hash).then(function(){ return hash; }); }); }
  function errText(e){ if(!e){ return 'Unknown error'; } if(e instanceof TypeError && /startsWith/.test(e.message||'')){ return 'Wallet received an invalid value (chain/address) from the server config. Check /payment/config returns string chainId, token and treasury.'; } if(e.code===4001){ return 'You rejected the request in MetaMask.'; } if(e.code===-32002){ return 'A MetaMask request is already open. Finish it and retry.'; } return e.message?e.message:String(e); }

  function mountWallet(btnId, opts){
    opts = opts || {};
    var btn = typeof btnId === 'string' ? document.getElementById(btnId) : btnId;
    function render(){
      if(!btn){ return; }
      if(!walletState.account){ btn.textContent = (opts.label||'Connect MetaMask'); btn.classList.remove('connected'); }
      else { var q=String.fromCharCode(39); btn.innerHTML = esc(shortAddr(walletState.account)) + ' <span class='+q+'bal'+q+'>' + (walletState.hiveWei==null?'...':esc(hiveStr(walletState.hiveWei))) + ' HIVE</span>'; btn.classList.add('connected'); }
    }
    function onClick(ev){
      if(walletState.account && opts.onDisconnect){ opts.onDisconnect(ev); return; }
      connect().then(function(){ render(); if(opts.onConnect){ opts.onConnect(); } }).catch(function(e){ alert(errText(e)); });
    }
    if(btn){ btn.onclick = onClick; }
    if(typeof ethereum !== 'undefined' && ethereum.on){
      ethereum.on('accountsChanged', function(a){ walletState.account=(a&&a.length)?a[0]:null; store.setWallet({ account: walletState.account }); render(); refreshBalance().then(render).catch(function(){}); });
      ethereum.on('chainChanged', function(){ refreshBalance().then(render).catch(function(){}); });
    }
    loadPay().catch(function(){});
    if(eth()){ rpc('eth_accounts').then(function(a){ if(a&&a.length){ walletState.account=a[0]; store.setWallet({ account: a[0] }); return refreshBalance().then(render); } render(); }).catch(function(){ render(); }); } else { render(); }
    return { render: render, connect: function(){ return connect().then(function(){ render(); return walletState; }); } };
  }
  return {
    API:API, store:store, threads:{ list:listThreads, get:getThread, active:activeThread, new:newThread, addMessage:addMessage, updateMessage:updateMessage, delete:deleteThread, setActive:setActiveThread },
    esc:esc, str:str, safeStartsWith:safeStartsWith, qs:qs, fmt:fmt, shortId:shortId, shortAddr:shortAddr, hiveStr:hiveStr, inline:inline, md:md, post:post, get:get,
    wallet:walletState, loadPay:loadPay, pay:function(){ return PAY; }, connect:connect, ensureChain:ensureChain, refreshBalance:refreshBalance, updateAgentPrice:updateAgentPrice, encUpdatePrice:encUpdatePrice,
    transferHive:transferHive, sendTx:sendTx, faucet:faucet, errText:errText, mountWallet:mountWallet, notify:notify, eth:eth, rpc:rpc, uid:uid
  };
})();


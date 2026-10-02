// Neural Hive math rendering for the chat markdown. Agents answer with LaTeX
// ($...$, $$...$$, \(...\), \[...\]). marked does not understand TeX, so NH.md (from
// app.js) is wrapped: each math span is pulled out before markdown parsing and rendered
// with KaTeX afterwards, so markdown can never corrupt an equation. If KaTeX is not
// loaded the original math text is preserved, never dropped.
(function(){
  var N=window.NH; if(!N||typeof N.md!=='function'){ return; }
  var SOH=String.fromCharCode(1);
  var TOK=SOH+'NHMATH'+SOH;
  var orig=N.md;
  function katexOk(){ return !!(window.katex && window.katex.renderToString); }
  function extract(src){
    var store=[];
    function put(tex,display){ var i=store.length; store.push({t:tex,d:display}); return TOK+i+TOK; }
    src=src.replace(/\$\$([\s\S]+?)\$\$/g,function(m,tex){ return put(tex,true); });
    src=src.replace(/\\\[([\s\S]+?)\\\]/g,function(m,tex){ return put(tex,true); });
    src=src.replace(/\\\(([\s\S]+?)\\\)/g,function(m,tex){ return put(tex,false); });
    src=src.replace(/(^|[^$\\])\$([^$\n]+?)\$/g,function(m,pre,tex){ return pre+put(tex,false); });
    return {s:src,store:store};
  }
  function render(html,store){
    if(!katexOk()||!store.length){ return html; }
    var re=new RegExp(TOK+'(\\d+)'+TOK,'g');
    return html.replace(re,function(m,i){
      var it=store[parseInt(i,10)]; if(!it){ return m; }
      try{ return window.katex.renderToString(it.t,{displayMode:it.d,throwOnError:false,strict:false,trust:false}); }
      catch(e){ return it.d?('$$'+it.t+'$$'):('$'+it.t+'$'); }
    });
  }
  N.md=function(text){
    var src=(text==null)?'':String(text);
    var ex=extract(src);
    var html=orig(ex.s);
    return render(html,ex.store);
  };
})();

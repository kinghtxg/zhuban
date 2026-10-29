/* 铸版官网交互脚本。
   独立成文件而不是内联：产品自己的 CSP 是 script-src 'self'，内联 <script> 会被浏览器
   直接拦掉——页面能打开，但所有动效与计数都不动，而且只在控制台里报错。*/
(function(){
  'use strict';
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* ---------- 滚动入场（必须第一个跑） ----------
     它决定正文能不能被看见：.reveal 默认 opacity:0，只有拿到 .in 才现身。
     2026-10-29 出过一次事故——下面的「导航高亮」因为一个外链 href 抛 SyntaxError，
     把整个 IIFE 带走，于是首屏以下 38 个区块永远停在 opacity:0，
     用户看到的是「官网内容没了好多」。**内容可见性不能依赖后面任何一段代码不犯错。** */
  var reveals = [].slice.call(document.querySelectorAll('.reveal'));
  if (!('IntersectionObserver' in window) || reduce){
    reveals.forEach(function(el){ el.classList.add('in'); });
  } else {
    var io = new IntersectionObserver(function(entries){
      entries.forEach(function(en){
        if (!en.isIntersecting) return;
        var el = en.target;
        // 同组元素错峰出现，避免整片一起弹
        var sibs = el.parentElement ? [].slice.call(el.parentElement.children).filter(function(c){ return c.classList.contains('reveal'); }) : [];
        var i = Math.max(0, sibs.indexOf(el));
        el.style.transitionDelay = Math.min(i * 70, 420) + 'ms';
        el.classList.add('in');
        io.unobserve(el);
      });
    }, {rootMargin:'0px 0px -12% 0px', threshold:0.08});
    reveals.forEach(function(el){ io.observe(el); });
  }

  /* ---------- 滚动进度与导航状态 ---------- */
  var nav = document.getElementById('nav');
  var bar = document.getElementById('bar');
  var links = [].slice.call(document.querySelectorAll('#navlinks a'));
  // 导航里混着外链（「文档」→ 线上手册）。querySelector('https://…') 会抛
  // SyntaxError: not a valid selector —— 只把 #锚点 当区块目标，其余一律 null。
  var sections = links.map(function(a){
    var href = a.getAttribute('href') || '';
    return href.charAt(0) === '#' ? document.querySelector(href) : null;
  });

  function onScroll(){
    var y = window.scrollY || document.documentElement.scrollTop;
    var h = document.documentElement.scrollHeight - window.innerHeight;
    bar.style.width = (h > 0 ? Math.min(100, (y / h) * 100) : 0) + '%';
    nav.classList.toggle('scrolled', y > 12);
    // 当前区块高亮：取「已经进入视口上部 40%」的最后一个
    var idx = -1;
    for (var i = 0; i < sections.length; i++){
      var el = sections[i];
      if (el && el.getBoundingClientRect().top <= window.innerHeight * 0.4) idx = i;
    }
    links.forEach(function(a, i){ a.classList.toggle('active', i === idx); });
  }
  window.addEventListener('scroll', onScroll, {passive:true});
  window.addEventListener('resize', onScroll);
  onScroll();

  /* ---------- 数字滚动 ---------- */
  function countUp(el){
    var target = parseFloat(el.getAttribute('data-count') || '0');
    var suffix = el.getAttribute('data-suffix') || '';
    if (reduce){ el.textContent = target + suffix; return; }
    var start = performance.now(), dur = 1500;
    function step(now){
      var p = Math.min(1, (now - start) / dur);
      var eased = 1 - Math.pow(1 - p, 3);
      el.textContent = Math.round(target * eased) + suffix;
      if (p < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }
  var nums = [].slice.call(document.querySelectorAll('[data-count]'));
  if (!('IntersectionObserver' in window)){
    nums.forEach(countUp);
  } else {
    var io2 = new IntersectionObserver(function(entries){
      entries.forEach(function(en){
        if (en.isIntersecting){ countUp(en.target); io2.unobserve(en.target); }
      });
    }, {threshold:0.6});
    nums.forEach(function(el){ io2.observe(el); });
  }

  /* ---------- 卡片跟随鼠标的高光 ---------- */
  document.querySelectorAll('.card').forEach(function(card){
    card.addEventListener('pointermove', function(e){
      var r = card.getBoundingClientRect();
      card.style.setProperty('--mx', (e.clientX - r.left) + 'px');
      card.style.setProperty('--my', (e.clientY - r.top) + 'px');
    });
  });

  /* ---------- 首屏设备卡：鼠标微倾 ---------- */
  var card = document.getElementById('forgeCard');
  var forge = document.getElementById('forge');
  if (card && forge && !reduce && window.matchMedia('(pointer:fine)').matches){
    forge.addEventListener('pointermove', function(e){
      var r = forge.getBoundingClientRect();
      var dx = (e.clientX - r.left) / r.width - 0.5;
      var dy = (e.clientY - r.top) / r.height - 0.5;
      card.style.transform = 'rotateY(' + (dx * 7).toFixed(2) + 'deg) rotateX(' + (-dy * 6).toFixed(2) + 'deg) translateZ(0)';
    });
    forge.addEventListener('pointerleave', function(){ card.style.transform = ''; });
  }

  /* ---------- 首屏「任务日志」逐行打字 + 进度条 ---------- */
  var lines = [].slice.call(document.querySelectorAll('#mcBody .mc-line'));
  var barEl = document.getElementById('mcBar');
  var stateEl = document.getElementById('mcState');
  var footEl = document.getElementById('mcFoot');
  var cntEl = document.getElementById('mcCount');

  function runConsole(){
    lines.forEach(function(l){ l.classList.remove('in'); });
    barEl.style.width = '0%';
    stateEl.textContent = 'running';
    stateEl.style.color = '';
    footEl.textContent = '渲染中…';
    cntEl.textContent = '0342 / 1000';

    if (reduce){
      lines.forEach(function(l){ l.classList.add('in'); });
      barEl.style.width = '100%';
      stateEl.textContent = 'succeeded';
      footEl.textContent = '完成 · 1000/1000';
      cntEl.textContent = '1000 / 1000';
      return;
    }

    var i = 0;
    (function next(){
      if (i >= lines.length){
        barEl.style.width = '100%';
        cntEl.textContent = '1000 / 1000';
        stateEl.textContent = 'succeeded';
        stateEl.style.color = '#7FE9B8';
        footEl.textContent = '完成 · 1000/1000';
        setTimeout(runConsole, 4200);
        return;
      }
      lines[i].classList.add('in');
      // 进度条跟着走：只在前几行之后推进，模拟"渲染中"的观感
      if (i >= 4) barEl.style.width = Math.min(96, 8 + (i - 3) * 14) + '%';
      i++;
      setTimeout(next, i === 1 ? 340 : 260);
    })();
  }
  if (lines.length) runConsole();

  /* ---------- 首屏粒子星图 ---------- */
  var cv = document.getElementById('dust');
  if (cv && !reduce){
    var ctx = cv.getContext('2d');
    var dpr = Math.min(2, window.devicePixelRatio || 1);
    var pts = [], W = 0, H = 0, raf = 0, running = true;

    function resize(){
      var r = cv.parentElement.getBoundingClientRect();
      W = cv.width = Math.floor(r.width * dpr);
      H = cv.height = Math.floor(r.height * dpr);
      cv.style.width = r.width + 'px';
      cv.style.height = r.height + 'px';
      var n = Math.min(70, Math.round(r.width / 16));
      pts = [];
      for (var i = 0; i < n; i++){
        pts.push({
          x: Math.random() * W, y: Math.random() * H,
          vx: (Math.random() - 0.5) * 0.22 * dpr,
          vy: (Math.random() - 0.5) * 0.22 * dpr,
          r: (Math.random() * 1.4 + 0.5) * dpr
        });
      }
    }

    function frame(){
      if (!running) return;
      ctx.clearRect(0, 0, W, H);
      var link = 130 * dpr;
      for (var i = 0; i < pts.length; i++){
        var p = pts[i];
        p.x += p.vx; p.y += p.vy;
        if (p.x < 0 || p.x > W) p.vx *= -1;
        if (p.y < 0 || p.y > H) p.vy *= -1;
        for (var j = i + 1; j < pts.length; j++){
          var q = pts[j], dx = p.x - q.x, dy = p.y - q.y;
          var d = Math.sqrt(dx * dx + dy * dy);
          if (d < link){
            ctx.strokeStyle = 'rgba(110,140,230,' + (0.16 * (1 - d / link)).toFixed(3) + ')';
            ctx.lineWidth = 0.7 * dpr;
            ctx.beginPath(); ctx.moveTo(p.x, p.y); ctx.lineTo(q.x, q.y); ctx.stroke();
          }
        }
        ctx.fillStyle = 'rgba(160,185,255,.55)';
        ctx.beginPath(); ctx.arc(p.x, p.y, p.r, 0, Math.PI * 2); ctx.fill();
      }
      raf = requestAnimationFrame(frame);
    }

    resize();
    frame();
    var rt;
    window.addEventListener('resize', function(){ clearTimeout(rt); rt = setTimeout(function(){ cancelAnimationFrame(raf); resize(); frame(); }, 180); });
    // 页面不可见时停掉，别在后台空转占 CPU
    document.addEventListener('visibilitychange', function(){
      if (document.hidden){ running = false; cancelAnimationFrame(raf); }
      else if (!running){ running = true; frame(); }
    });
  }

  /* ---------- 复制仓库地址 ---------- */
  var copyBtn = document.getElementById('copyBtn');
  var toast = document.getElementById('toast');
  function showToast(msg){
    toast.textContent = msg;
    toast.classList.add('show');
    clearTimeout(showToast.t);
    showToast.t = setTimeout(function(){ toast.classList.remove('show'); }, 1900);
  }
  if (copyBtn){
    copyBtn.addEventListener('click', function(){
      var text = 'https://github.com/kinghtxg/zhuban';
      if (navigator.clipboard && navigator.clipboard.writeText){
        navigator.clipboard.writeText(text).then(function(){ showToast('已复制仓库地址'); }, function(){ showToast(text); });
      } else {
        // 非安全上下文（http）下 clipboard API 不可用，退回到选中文本
        var ta = document.createElement('textarea');
        ta.value = text; document.body.appendChild(ta); ta.select();
        try { document.execCommand('copy'); showToast('已复制仓库地址'); } catch (e) { showToast(text); }
        document.body.removeChild(ta);
      }
    });
  }
})();

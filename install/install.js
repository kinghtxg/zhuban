/* 铸版安装向导 · 交互脚本
   ---------------------------------------------------------------
   独立成文件而不是内联：产品全站 CSP 是 script-src 'self'，内联 <script>
   会被浏览器直接拦掉——页面能打开，但一个按钮都不动，且只在控制台报错。
   （官网 site/landing.js 是同一个原因。）

   三步的状态全部在这一个文件里：
     1 管理员  → POST /api/v1/auth/setup（成功即带会话 cookie）
     2 邮件    → PUT /api/v1/admin/mail/settings（可跳过）
     3 完成    → 展示地址与版本

   注意第 2 步用的是**需要登录**的管理接口：第 1 步成功时服务端已经
   下发了会话 cookie，所以这里不需要再登录一次。
   --------------------------------------------------------------- */
(function () {
  'use strict';

  var $ = function (sel, root) { return (root || document).querySelector(sel); };
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  var state = {
    step: 1,
    adminEmail: '',
    username: '',
    version: '',
    mailConfigured: false,
    baseURL: location.origin
  };

  /* ---------- 通用：提示与错误 ---------- */

  var alertBox = $('#alert');

  function alertShow(msg, kind) {
    alertBox.textContent = msg;
    alertBox.setAttribute('data-kind', kind || 'err');
    alertBox.setAttribute('data-show', '');
  }

  function alertHide() {
    alertBox.removeAttribute('data-show');
  }

  function fieldErr(name, msg) {
    var box = $('[data-err="' + name + '"]');
    var input = $('#' + name);
    if (box) {
      box.textContent = msg || '';
      if (msg) { box.setAttribute('data-show', ''); } else { box.removeAttribute('data-show'); }
    }
    if (input) {
      if (msg) { input.setAttribute('aria-invalid', 'true'); } else { input.removeAttribute('aria-invalid'); }
    }
  }

  function clearErrs(form) {
    $$('[data-err]', form).forEach(function (b) { b.removeAttribute('data-show'); b.textContent = ''; });
    $$('.input', form).forEach(function (i) { i.removeAttribute('aria-invalid'); });
  }

  function busy(btn, on) {
    if (!btn) return;
    btn.disabled = !!on;
    if (on) { btn.setAttribute('data-busy', ''); } else { btn.removeAttribute('data-busy'); }
  }

  /* ---------- 通用：请求 ---------- */

  // request 统一处理非 2xx：把服务端的 error.code / error.message 翻成人话。
  //
  // 这里不吞状态码：安装阶段最常见的失败恰恰是「403 已被安装过」
  // 和「422 密码不合规」，两者要给出完全不同的指引。
  function request(method, path, body) {
    var opt = {
      method: method,
      credentials: 'same-origin',
      headers: { 'Accept': 'application/json' }
    };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    return fetch(path, opt).then(function (res) {
      var ct = res.headers.get('content-type') || '';
      var parse = ct.indexOf('json') >= 0 ? res.json().catch(function () { return null; })
                                          : res.text().then(function (t) { return { message: t }; });
      return parse.then(function (data) {
        if (res.ok) return data;
        var err = new Error((data && (data.message || (data.error && data.error.message))) || ('请求失败（' + res.status + '）'));
        err.status = res.status;
        err.code = data && (data.code || (data.error && data.error.code));
        err.data = data;
        throw err;
      });
    });
  }

  /* ---------- 步骤切换 ---------- */

  function goStep(n) {
    state.step = n;
    $$('.panel').forEach(function (p) {
      if (p.getAttribute('data-panel') === String(n)) { p.setAttribute('data-show', ''); }
      else { p.removeAttribute('data-show'); }
    });
    $$('.step').forEach(function (s) {
      var i = Number(s.getAttribute('data-for'));
      if (i < n) { s.setAttribute('data-state', 'done'); }
      else if (i === n) { s.setAttribute('data-state', 'active'); }
      else { s.removeAttribute('data-state'); }
    });
    alertHide();
    window.scrollTo({ top: 0, behavior: 'smooth' });

    var focus = $('.panel[data-show] .input');
    if (focus) { focus.focus({ preventScroll: true }); }
  }

  /* ---------- 步骤 1：管理员 ---------- */

  function scorePassword(pw) {
    // 只做"看得见的提示"，真正的规则由服务端判定。
    // 前端复刻一份完整规则只会出现"两处不一致"的经典问题。
    if (!pw || pw.length < 8) return 0;
    var hasL = /[A-Za-z]/.test(pw), hasD = /[0-9]/.test(pw);
    if (!hasL || !hasD) return 1;
    var extra = /[^A-Za-z0-9]/.test(pw) || pw.length >= 14;
    return extra ? 3 : 2;
  }

  var meter = $('#meter');

  $('#password').addEventListener('input', function () {
    meter.setAttribute('data-score', String(scorePassword(this.value)));
  });

  $('#form-admin').addEventListener('submit', function (e) {
    e.preventDefault();
    var btn = $('#btn-admin');
    clearErrs(this);
    alertHide();

    var username = $('#username').value.trim();
    var pw = $('#password').value;
    var pw2 = $('#password2').value;
    var email = $('#email').value.trim();
    var display = $('#display_name').value.trim();

    // 本地先挡一遍：这些错误服务端也会拒，但让用户等一个往返没有意义。
    var bad = false;
    if (!username) { fieldErr('username', '请填写用户名'); bad = true; }
    else if (username.length < 3) { fieldErr('username', '用户名至少 3 位'); bad = true; }
    else if (!/^[\p{L}\p{N}_-]+$/u.test(username)) { fieldErr('username', '只能含字母、数字、下划线、连字符'); bad = true; }
    if (!pw) { fieldErr('password', '请填写密码'); bad = true; }
    else if (pw.length < 8) { fieldErr('password', '密码至少 8 位'); bad = true; }
    else if (!/[A-Za-z]/.test(pw) || !/[0-9]/.test(pw)) { fieldErr('password', '需同时包含字母与数字'); bad = true; }
    if (pw && pw2 !== pw) { fieldErr('password2', '两次输入的密码不一致'); bad = true; }
    if (email && email.indexOf('@') < 1) { fieldErr('email', '邮箱格式不对'); bad = true; }
    if (bad) return;

    busy(btn, true);
    var payload = { username: username, password: pw };
    if (display) { payload.display_name = display; }
    if (email) { payload.email = email; }

    request('POST', '/api/v1/auth/setup', payload).then(function () {
      state.adminEmail = email;
      state.username = username;
      if (!email) { /* 没填邮箱就不预填测试收件人 */ }
      goStep(2);
      $('#test_to').value = email || '';
    }).catch(function (err) {
      if (err.status === 404) {
        // 已被安装过：说明这个页面是刷出来的旧标签页，或者两个人同时装。
        alertShow('这个实例已经完成初始化了。刷新页面会跳到登录页。', 'err');
        setTimeout(function () { location.replace('/'); }, 2600);
        return;
      }
      if (err.status === 429) { alertShow('尝试过于频繁，请稍等一分钟再试。', 'err'); return; }
      if (err.status === 409) { fieldErr('username', '这个用户名已被占用，换一个'); return; }
      var fields = err.data && (err.data.fields || (err.data.error && err.data.error.fields));
      if (fields && fields.length) {
        fields.forEach(function (f) { fieldErr(f.path || f.field, f.message); });
        return;
      }
      alertShow(err.message || '创建失败，请重试', 'err');
    }).then(function () { busy(btn, false); });
  });

  /* ---------- 步骤 2：邮件 ---------- */

  function mailPayload() {
    var host = $('#smtp_host').value.trim();
    if (!host) return null; // 主机为空 = 不配置

    var p = {
      smtp_host: host,
      smtp_port: Number($('#smtp_port').value) || 587,
      smtp_tls: $('#smtp_tls').value,
      smtp_user: $('#smtp_user').value.trim(),
      smtp_from: $('#smtp_from').value.trim(),
      mail_allowed_domains: $('#mail_allowed_domains').value.trim()
    };
    // 密码留空表示"不修改"。首次安装时留空即视为不设密码（内网中继场景）。
    var pw = $('#smtp_password').value;
    if (pw) { p.smtp_password = pw; }
    return p;
  }

  $('#btn-test').addEventListener('click', function () {
    var btn = this;
    clearErrs($('#form-mail'));
    alertHide();

    var to = $('#test_to').value.trim();
    if (!to || to.indexOf('@') < 1) {
      fieldErr('test_to', '请先填一个收件人邮箱');
      return;
    }
    var payload = mailPayload();
    if (!payload) {
      fieldErr('smtp_host', '发测试邮件前需要先填 SMTP 主机');
      return;
    }

    busy(btn, true);
    // 先保存再测：测试用的是"库里那份配置"，不保存就测等于测了个空。
    // 这个顺序也保证了用户点完测试之后，配置已经落盘，可以直接继续。
    request('PUT', '/api/v1/admin/mail/settings', payload)
      .then(function () { return request('POST', '/api/v1/admin/mail/test', { to: to }); })
      .then(function () {
        state.mailConfigured = true;
        alertShow('测试邮件已发出，去收件箱确认一下（可能在垃圾箱）。', 'ok');
      })
      .catch(function (err) {
        alertShow(err.message || '发送失败，检查一下主机、端口与授权码', 'err');
      })
      .then(function () { busy(btn, false); });
  });

  $('#form-mail').addEventListener('submit', function (e) {
    e.preventDefault();
    var btn = $('#btn-mail');
    clearErrs(this);
    alertHide();

    var payload = mailPayload();
    if (!payload) {
      // 没填主机却点了"保存并继续"：不要静默跳过，说清发生了什么。
      fieldErr('smtp_host', '没填主机。不打算配邮件的话，点「跳过」');
      return;
    }
    busy(btn, true);
    request('PUT', '/api/v1/admin/mail/settings', payload)
      .then(function () {
        state.mailConfigured = true;
        finish();
      })
      .catch(function (err) {
        alertShow(err.message || '保存失败', 'err');
      })
      .then(function () { busy(btn, false); });
  });

  $('#btn-skip').addEventListener('click', function () {
    state.mailConfigured = false;
    finish();
  });

  /* ---------- 步骤 3：完成 ---------- */

  function finish() {
    $('#done-url').textContent = state.baseURL;
    $('#done-mail').textContent = state.mailConfigured ? '已配置' : '未配置（可稍后在管理里开）';
    $('#done-version').textContent = state.version || '—';
    goStep(3);
    var go = $('#btn-go');
    if (go) { go.focus({ preventScroll: true }); }
  }

  /* ---------- 启动：先确认这个实例还需要安装 ---------- */

  // 服务端在已初始化时会把 /install 变成 404，正常走不到这里。
  // 但页面可能是从缓存/旧标签页打开的，所以自己也查一次：
  // 让用户看到"已经装过了，去登录"，而不是一个填完才报错的表单。
  request('GET', '/api/v1/auth/setup-status').then(function (st) {
    if (st && st.initialized) {
      alertShow('这个实例已经完成初始化了，正在跳转到登录页…', 'ok');
      setTimeout(function () { location.replace('/'); }, 2000);
      return;
    }
    return request('GET', '/api/v1/version').then(function (v) {
      if (v && v.version) { state.version = v.dev ? 'dev（本地构建）' : 'v' + v.version; }
    }).catch(function () { /* 版本拿不到不影响安装 */ });
  }).catch(function () {
    // setup-status 都拿不到：多半是服务还没起好。让用户重试，不要装作没事。
    alertShow('连不上服务端，确认容器已经启动后再刷新本页。', 'err');
  });
})();

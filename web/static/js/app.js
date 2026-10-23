/* mini-aigw 共通スクリプト (インライン JS を置き換える data-* 駆動) */
(function () {
  'use strict';

  // ---------- Toast ----------
  function toast(message, kind) {
    var area = document.getElementById('toast-area');
    if (!area) return;
    var el = document.createElement('div');
    el.className = 'app-toast' + (kind ? ' toast-' + kind : '');
    el.setAttribute('role', kind === 'error' ? 'alert' : 'status');
    var icon = kind === 'error' ? 'bi-exclamation-circle' : (kind === 'success' ? 'bi-check-circle' : 'bi-info-circle');
    var i = document.createElement('i');
    i.className = 'bi ' + icon;
    var span = document.createElement('span');
    span.textContent = message;
    el.appendChild(i);
    el.appendChild(span);
    area.appendChild(el);
    setTimeout(function () { el.remove(); }, kind === 'error' ? 6000 : 3000);
  }
  window.appToast = toast;

  // ---------- Clipboard (HTTP 非セキュアコンテキスト向けフォールバック付) ----------
  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      try {
        document.execCommand('copy') ? resolve() : reject(new Error('copy failed'));
      } catch (e) {
        reject(e);
      } finally {
        ta.remove();
      }
    });
  }

  // <button data-copy-target="#id"> または data-copy-text="..."
  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest('[data-copy-target],[data-copy-text]');
    if (!btn) return;
    var text = btn.getAttribute('data-copy-text');
    if (text === null) {
      var t = document.querySelector(btn.getAttribute('data-copy-target'));
      text = t ? (t.value !== undefined && t.tagName !== 'PRE' && t.tagName !== 'CODE' ? t.value : t.textContent) : '';
    }
    copyText(text.trim()).then(
      function () { toast('クリップボードにコピーしました', 'success'); },
      function () { toast('コピーに失敗しました。手動で選択してコピーしてください', 'error'); }
    );
  });

  // <button data-clear-target="#id"> 対象の中身を空にする
  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest('[data-clear-target]');
    if (!btn) return;
    var t = document.querySelector(btn.getAttribute('data-clear-target'));
    if (t) t.innerHTML = '';
  });

  // ---------- htmx エラー通知 ----------
  document.addEventListener('htmx:responseError', function (ev) {
    var xhr = ev.detail.xhr;
    var msg = '処理に失敗しました (HTTP ' + xhr.status + ')';
    if (xhr.status === 403) msg = '権限がありません、またはセッションが無効です';
    if (xhr.status === 401) msg = 'ログインが必要です';
    toast(msg, 'error');
  });
  document.addEventListener('htmx:sendError', function () {
    toast('サーバーに接続できません', 'error');
  });

  // ---------- 削除確認モーダル (hx-confirm の置き換え) ----------
  // htmx の confirm イベントを横取りし Bootstrap モーダルで確認する
  document.addEventListener('htmx:confirm', function (ev) {
    if (!ev.detail.question) return;
    var modalEl = document.getElementById('confirmModal');
    if (!modalEl || typeof bootstrap === 'undefined') return; // 標準 confirm にフォールバック
    ev.preventDefault();
    modalEl.querySelector('[data-confirm-message]').textContent = ev.detail.question;
    var modal = bootstrap.Modal.getOrCreateInstance(modalEl);
    var ok = modalEl.querySelector('[data-confirm-ok]');
    var handler = function () {
      ok.removeEventListener('click', handler);
      modal.hide();
      ev.detail.issueRequest(true);
    };
    ok.addEventListener('click', handler);
    modalEl.addEventListener('hidden.bs.modal', function () { ok.removeEventListener('click', handler); }, { once: true });
    modal.show();
  });
})();

// ---------- 追加ビヘイビア (data-* 駆動) ----------
(function () {
  'use strict';

  // 自動表示モーダル (htmx で差し込まれた発行完了ダイアログ等)
  function showAutoModals(root) {
    (root || document).querySelectorAll('.modal[data-auto-show]').forEach(function (el) {
      if (typeof bootstrap === 'undefined') return;
      el.removeAttribute('data-auto-show');
      bootstrap.Modal.getOrCreateInstance(el).show();
    });
  }
  document.addEventListener('htmx:afterSettle', function (ev) { showAutoModals(ev.target); });
  document.addEventListener('DOMContentLoaded', function () { showAutoModals(); });

  // フォーム送信時に対象モーダルを閉じる: <form data-modal-hide-on-submit="#id">
  document.addEventListener('submit', function (ev) {
    var sel = ev.target.getAttribute && ev.target.getAttribute('data-modal-hide-on-submit');
    if (!sel || typeof bootstrap === 'undefined') return;
    var m = bootstrap.Modal.getInstance(document.querySelector(sel));
    if (m) m.hide();
  });

  // ---------- APIキーフォームのモード切替 (ナレッジ検索 / LLM単体) ----------
  var EXAMPLES = {
    business: 'あなたは業務アシスタントです。丁寧な敬語を用い、要点を整理して箇条書きで簡潔・明確に回答してください。',
    general: 'あなたは親切で誠実なAIアシスタントです。質問に対して具体的かつ分かりやすく回答してください。'
  };
  var HINT_ON = 'ナレッジ検索有効時: 組み込みプロンプトに上乗せされます。口調やペルソナ等の追加指示のみ記述してください。';
  var HINT_OFF = 'LLM単体モード: 役割やルールを固定したい場合は本欄に入力してください。';

  function refreshKeyForm(form) {
    var boxes = form.querySelectorAll('input[name="allowed_groups"]');
    var on = Array.prototype.some.call(boxes, function (c) { return c.checked; });
    var badge = form.querySelector('[data-mode-badge]');
    if (badge) {
      badge.className = 'status-badge ' + (on ? 'status-success' : 'status-neutral');
      badge.textContent = on ? 'ナレッジ検索 有効' : 'LLM単体モード';
    }
    var hint = form.querySelector('[data-mode-hint]');
    if (hint) hint.textContent = on ? HINT_ON : HINT_OFF;
    var box = form.querySelector('[data-builtin-box]');
    if (box) box.hidden = !on;
  }

  document.addEventListener('change', function (ev) {
    var form = ev.target.closest && ev.target.closest('[data-key-form]');
    if (form && ev.target.name === 'allowed_groups') refreshKeyForm(form);
  });
  document.addEventListener('click', function (ev) {
    var form = ev.target.closest && ev.target.closest('[data-key-form]');
    if (!form) return;
    var all = ev.target.closest('[data-check-all]');
    var none = ev.target.closest('[data-check-none]');
    if (all || none) {
      form.querySelectorAll('input[name="allowed_groups"]').forEach(function (c) { c.checked = !!all; });
      refreshKeyForm(form);
    }
    var ins = ev.target.closest('[data-insert-example]');
    if (ins) {
      var ta = document.querySelector(ins.getAttribute('data-insert-target'));
      var txt = EXAMPLES[ins.getAttribute('data-insert-example')];
      if (ta && txt) { ta.value = txt; ta.focus(); }
    }
  });
  document.addEventListener('DOMContentLoaded', function () {
    document.querySelectorAll('[data-key-form]').forEach(refreshKeyForm);
  });

  // フォーム送信後にリセット: <form data-reset-after-submit>
  document.addEventListener('htmx:afterRequest', function (ev) {
    var el = ev.detail.elt;
    if (el && el.hasAttribute && el.hasAttribute('data-reset-after-submit') && ev.detail.successful) {
      el.reset();
      if (el.matches('[data-key-form]')) refreshKeyForm(el);
    }
  });
})();

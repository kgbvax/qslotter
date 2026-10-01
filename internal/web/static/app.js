// Global error surfacing for htmx actions: a failed request (printer missing,
// declined card, server down) shows a toast instead of doing nothing.
// tr translates a message the page handed over (window.qslT, see jsStrings in
// server.go); English otherwise.
function qslTr(text) {
  var t = (window.qslT && window.qslT[text]) || text;
  var args = Array.prototype.slice.call(arguments, 1);
  return t.replace(/%[sd]/g, function () { return args.length ? String(args.shift()) : ''; });
}

(function () {
  var toast = null, timer = null;
  function show(msg) {
    if (!toast) {
      toast = document.createElement('div');
      toast.className = 'toast';
      document.body.appendChild(toast);
    }
    toast.textContent = msg;
    toast.classList.add('show');
    clearTimeout(timer);
    timer = setTimeout(function () { toast.classList.remove('show'); }, 8000);
  }
  document.addEventListener('htmx:responseError', function (ev) {
    var xhr = ev.detail.xhr;
    var text = (xhr.responseText || '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
    if (text.length > 220) text = text.slice(0, 220) + '...';
    show(qslTr('Error') + ' ' + xhr.status + ': ' + (text || xhr.statusText));
  });
  // Server-sent notices (HX-Trigger: {"qslNotice": "..."}) use the same toast.
  document.addEventListener('qslNotice', function (ev) {
    show(ev.detail && ev.detail.value ? ev.detail.value : '');
  });
  document.addEventListener('htmx:sendError', function () {
    show(qslTr('Network error - is the qslotter server running?'));
  });
})();

// In the desktop app window (glaze sets window.__webview__) a target=_blank
// link has nowhere to go: hand it to the system browser via the server.
(function () {
  document.addEventListener('click', function (ev) {
    if (!window.__webview__) return;
    var a = ev.target.closest && ev.target.closest('a[target="_blank"]');
    if (!a || !a.href) return;
    ev.preventDefault();
    var body = new URLSearchParams({ url: a.href });
    fetch('/api/open-external', { method: 'POST', body: body }).then(function (r) {
      if (!r.ok) r.text().then(function (t) {
        document.dispatchEvent(new CustomEvent('qslNotice', { detail: { value: t.trim() } }));
      });
    });
  });
})();


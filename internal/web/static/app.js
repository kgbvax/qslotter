// Global error surfacing for htmx actions: a failed request (printer missing,
// declined card, server down) shows a toast instead of doing nothing.
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
    show('Error ' + xhr.status + ': ' + (text || xhr.statusText));
  });
  // Server-sent notices (HX-Trigger: {"qslNotice": "..."}) use the same toast.
  document.addEventListener('qslNotice', function (ev) {
    show(ev.detail && ev.detail.value ? ev.detail.value : '');
  });
  document.addEventListener('htmx:sendError', function () {
    show('Network error - is the qslotter server running?');
  });
})();

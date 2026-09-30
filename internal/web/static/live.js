// Live updates for the queue pages, driven by the /events SSE stream.
//
// The server publishes "queue_changed" ({key, to}) whenever a card moves and
// "station_updated" (the call) when a QRZ lookup finishes. Each page type
// (body[data-live]) reacts to the events that concern it:
//   list      decision-queue table: rows appear, refresh or vanish
//   decide    decide card view: loads a card when idle; follows a card that was
//             handled in another window
//   worklist  work-queue lists: reload <main>
//   workcard  work card view: like decide, for decided cards
(function () {
  var mode = document.body.dataset.live;
  if (!mode || !window.EventSource) return;
  var es = new EventSource('/events');

  function parse(e) { try { return JSON.parse(e.data); } catch (_) { return null; } }
  function byId(id) { return document.getElementById(id); }
  // Our own click already swaps its response in; the event it causes arrives
  // at the same moment. Reacting immediately would race that swap (htmx
  // swapError on a detached target), so react after it settled and only if
  // the page still needs it.
  var SETTLE_MS = 400;
  function settled(fn) { setTimeout(fn, SETTLE_MS); }

  // Nav badges follow every move (debounced).
  var navTimer = null;
  function refreshNav() {
    clearTimeout(navTimer);
    navTimer = setTimeout(function () {
      fetch('/nav').then(function (r) { return r.ok ? r.text() : ''; }).then(function (html) {
        var nav = document.querySelector('header.site nav');
        if (nav && html.trim()) nav.outerHTML = html.trim();
      });
    }, 250);
  }
  es.addEventListener('queue_changed', refreshNav);

  if (mode === 'list') {
    var body = byId('queue-body');
    var compact = document.body.classList.contains('compact');
    var countEl = byId('queue-count'), emptyEl = byId('empty-msg');
    var sync = function () {
      var n = body.querySelectorAll('tr').length;
      if (countEl) countEl.textContent = n;
      if (emptyEl) emptyEl.hidden = n > 0;
    };
    new MutationObserver(sync).observe(body, { childList: true }); // also covers htmx row removal
    var put = function (key) {
      var url = '/queue/row?key=' + encodeURIComponent(key) + (compact ? '&compact=1' : '');
      fetch(url, { headers: { 'HX-Request': 'true' } })
        .then(function (r) { return r.status === 200 ? r.text() : ''; })
        .then(function (html) {
          var old = byId('row-' + key);
          if (!html.trim()) { if (old) old.remove(); return; }
          var tmp = document.createElement('tbody');
          tmp.innerHTML = html.trim();
          var row = tmp.firstElementChild;
          if (!row) return;
          if (old) old.replaceWith(row); else body.prepend(row); // newest QSO on top
          if (window.htmx) htmx.process(row);
        });
    };
    es.addEventListener('queue_changed', function (e) {
      var d = parse(e); if (!d) return;
      if (d.to === 'queued') { put(d.key); return; }
      settled(function () { var old = byId('row-' + d.key); if (old) old.remove(); });
    });
    es.addEventListener('station_updated', function (e) {
      var call = e.data.toUpperCase();
      body.querySelectorAll('tr').forEach(function (tr) {
        var a = tr.querySelector('a[href^="/station/"]');
        if (a && a.textContent.trim().toUpperCase() === call) put(tr.id.slice(4));
      });
    });
    return;
  }

  if (mode === 'worklist') {
    var reload = function () {
      fetch(location.pathname).then(function (r) { return r.text(); }).then(function (html) {
        var doc = new DOMParser().parseFromString(html, 'text/html');
        var fresh = doc.querySelector('main'), cur = document.querySelector('main');
        if (fresh && cur) { cur.innerHTML = fresh.innerHTML; if (window.htmx) htmx.process(cur); }
      });
    };
    es.addEventListener('queue_changed', function () { settled(reload); });
    return;
  }

  // Card views (decide / workcard).
  var cfg = mode === 'decide'
    ? { id: 'decide', base: '/decide', enters: 'queued' }
    : { id: 'workcard', base: '/work/card', enters: 'decided' };
  function reloadCard(key) {
    var el = byId(cfg.id);
    var q = [];
    if (key) q.push('key=' + encodeURIComponent(key));
    if (el && el.dataset.filter) q.push('filter=' + encodeURIComponent(el.dataset.filter));
    htmx.ajax('GET', cfg.base + (q.length ? '?' + q.join('&') : ''), { target: '#' + cfg.id, swap: 'outerHTML' });
  }
  function typing(el) { return el && el.querySelector('input:focus'); }
  es.addEventListener('queue_changed', function (e) {
    var d = parse(e);
    if (!d) return;
    settled(function () {
      var el = byId(cfg.id); // look up again: our own action may have replaced it
      if (!el || typing(el)) return;
      var shown = el.dataset.qslkey;
      if (!shown) { if (d.to === cfg.enters) reloadCard(); }          // idle: a card arrived
      else if (d.key === shown && d.to !== cfg.enters) reloadCard();  // handled in another window
    });
  });
  if (mode === 'decide') {
    es.addEventListener('station_updated', function (e) {
      var el = byId(cfg.id);
      if (el && !typing(el) && el.dataset.call && el.dataset.call.toUpperCase() === e.data.toUpperCase()) {
        reloadCard(el.dataset.qslkey);
      }
    });
  }
})();

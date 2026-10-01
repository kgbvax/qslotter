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
    // Unsaved choices on the list (route, manager, batch ticks) survive a
    // reload: only fields the operator changed are carried over, by name.
    var snapshot = function (root) {
      var st = {};
      root.querySelectorAll('select.route-sel, input.mgr-in, input[type="checkbox"][name="keys"]').forEach(function (f) {
        if (f.type === 'checkbox') { if (f.checked !== f.defaultChecked) st['c:' + f.value] = f.checked; return; }
        var changed = f.tagName === 'SELECT'
          ? !(f.selectedOptions[0] && f.selectedOptions[0].defaultSelected)
          : f.value !== f.defaultValue;
        if (changed) st['v:' + f.name] = f.value;
      });
      return st;
    };
    var restore = function (root, st) {
      root.querySelectorAll('select.route-sel, input.mgr-in, input[type="checkbox"][name="keys"]').forEach(function (f) {
        if (f.type === 'checkbox') { if (('c:' + f.value) in st) f.checked = st['c:' + f.value]; return; }
        if (('v:' + f.name) in st) f.value = st['v:' + f.name];
      });
    };
    var busy = function () { return document.querySelector('main select:focus, main input:focus:not([type="checkbox"])'); };
    var reload = function () {
      if (busy()) { settled(reload); return; } // not while a field is being edited
      fetch(location.pathname).then(function (r) { return r.text(); }).then(function (html) {
        var doc = new DOMParser().parseFromString(html, 'text/html');
        var fresh = doc.querySelector('main'), cur = document.querySelector('main');
        if (!fresh || !cur) return;
        var st = snapshot(cur);
        cur.innerHTML = fresh.innerHTML;
        restore(cur, st);
        if (window.htmx) htmx.process(cur);
      });
    };
    es.addEventListener('queue_changed', function (e) {
      var d = parse(e);
      // A QSO entering the Inbox does not touch the Desk unless it was on it.
      if (d && d.to === 'queued' && !document.querySelector('input[name="key"][value="' + CSS.escape(d.key) + '"]')) return;
      settled(reload);
    });
    return;
  }

  // Card views (decide / workcard).
  var cfg = mode === 'decide'
    ? { id: 'decide', base: '/decide', enters: 'queued' }
    : { id: 'workcard', base: '/work/card', enters: 'decided' };
  // reloadCard shows the card with key (or the first one). keep=true carries
  // the operator's unsaved choices on a Desk card (route, manager, unticked
  // QSOs) through the reload.
  function reloadCard(key, keep) {
    var el = byId(cfg.id);
    var q = [];
    if (key) q.push('key=' + encodeURIComponent(key));
    if (el && el.dataset.filter) q.push('filter=' + encodeURIComponent(el.dataset.filter));
    if (keep && el && mode === 'workcard') {
      // Only what the operator changed; untouched fields take the server's
      // (possibly fresher) preselection.
      var r = el.querySelector('#route-pick input[name="route"]:checked');
      if (r && !r.defaultChecked) q.push('route=' + encodeURIComponent(r.value));
      var m = el.querySelector('#route-pick .mgr-in');
      if (m && m.value !== m.defaultValue) q.push('manager=' + encodeURIComponent(m.value));
      var skip = [];
      el.querySelectorAll('input[type="checkbox"][name="key"]').forEach(function (c) { if (!c.checked) skip.push(c.value); });
      if (skip.length) q.push('skip=' + encodeURIComponent(skip.join(',')));
    }
    htmx.ajax('GET', cfg.base + (q.length ? '?' + q.join('&') : ''), { target: '#' + cfg.id, swap: 'outerHTML' });
  }
  function typing(el) {
    if (!el) return false;
    if (el.querySelector('input:focus:not([type="radio"]):not([type="checkbox"]), select:focus, textarea:focus')) return true;
    var req = el.querySelector('#req');
    return !!(req && !req.hidden); // filling in a request: do not throw it away
  }
  function keysOf(el) { return (el.dataset.keys || el.dataset.qslkey || '').split(' ').filter(Boolean); }
  es.addEventListener('queue_changed', function (e) {
    var d = parse(e);
    if (!d) return;
    settled(function () {
      var el = byId(cfg.id); // look up again: our own action may have replaced it
      if (!el || typing(el)) return;
      var shown = el.dataset.qslkey;
      if (!shown) { if (d.to === cfg.enters) reloadCard(); return; } // idle: a card arrived
      var keys = keysOf(el);
      if (keys.indexOf(d.key) >= 0 && d.to !== cfg.enters) {
        // handled in another window: what is left of this card, else the next
        var rest = keys.filter(function (k) { return k !== d.key; });
        reloadCard(rest[0], rest.length > 0);
      } else if (mode === 'workcard' && d.to === cfg.enters && el.dataset.call &&
                 d.key.split('|')[0].toUpperCase() === el.dataset.call.toUpperCase()) {
        reloadCard(shown, true); // another QSO with this station joined the card
      }
    });
  });
  es.addEventListener('station_updated', function (e) {
    var el = byId(cfg.id);
    if (!el || !el.dataset.qslkey) return;
    var call = e.data.toUpperCase();
    // The manager's address landed: refresh just that block (keeps focus).
    if (mode === 'workcard' && (el.dataset.mgr || '').toUpperCase() === call && (el.dataset.call || '').toUpperCase() !== call) {
      if (byId('mgr-addr')) htmx.ajax('GET', '/work/manager?manager=' + encodeURIComponent(call), { target: '#mgr-addr', swap: 'innerHTML' });
      return;
    }
    if (typing(el)) return;
    if ((el.dataset.call || '').toUpperCase() === call) {
      reloadCard(el.dataset.qslkey, mode === 'workcard');
    }
  });
})();

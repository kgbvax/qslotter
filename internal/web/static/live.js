// Live updates for the queue pages, driven by the /events SSE stream.
//
// The server publishes "queue_changed" ({key, to}) whenever a card moves and
// "station_updated" (the call) when a QRZ lookup finishes. Each page type
// (body[data-live]) reacts to the events that concern it:
//   list      decision-queue table: rows appear, refresh or vanish
//   decide    decide card view: loads a card when idle; follows a card that was
//             handled in another window
//   workcard  work card view: like decide, for decided cards
//   (Inbox pages also keep the "QSO in progress" box current: current_contact)
//   md-inbox, md-desk  master-detail pages: the list reloads, the detail pane
//             follows the selection (and moves on when its card left); the
//             Desk's print queue section reloads too
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

  // The Print queue view (data-live=printq) follows every move, keeping the
  // ticks of the open run; its button in the band of every Desk page keeps
  // its count.
  var pqTimer = null;
  function reloadPrintQueue() {
    clearTimeout(pqTimer);
    pqTimer = setTimeout(function () {
      var badge = byId('printq-view');
      if (badge) {
        fetch('/work/printq?badge=1').then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
          var cur = byId('printq-view');
          if (html === null || !cur) return;
          var tmp = document.createElement('div');
          tmp.innerHTML = html.trim();
          if (tmp.firstElementChild) cur.replaceWith(tmp.firstElementChild);
        });
      }
      var pq = byId('printq');
      if (!pq || mode !== 'printq') return;
      var ticks = {};
      pq.querySelectorAll('input[name="lead"]:checked').forEach(function (c) { ticks[c.value] = true; });
      fetch('/work/printq', { headers: { 'HX-Request': 'true' } }).then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
        var cur = byId('printq');
        if (html === null || !cur) return;
        var tmp = document.createElement('div');
        tmp.innerHTML = html.trim();
        var fresh = tmp.firstElementChild;
        if (!fresh) return;
        fresh.querySelectorAll('input[name="lead"]').forEach(function (c) { if (ticks[c.value]) c.checked = true; });
        cur.replaceWith(fresh);
        if (window.htmx) htmx.process(fresh);
        var empty = byId('printq-empty');
        if (empty) empty.hidden = !fresh.hidden;
      });
    }, 150);
  }
  es.addEventListener('queue_changed', function () { settled(reloadPrintQueue); });

  // The QSO in progress (Inbox pages): the box follows the logger's entry
  // field and the station's QRZ data.
  function reloadCurrent() {
    var box = byId('current');
    if (!box) return;
    var url = '/queue/current' + (box.dataset.compact ? '?compact=1' : '');
    htmx.ajax('GET', url, { source: box, target: '#current', swap: 'outerHTML' });
  }
  if (byId('current')) {
    var curTimer = null; // a burst of events (typing, a logged QSO) reloads once
    es.addEventListener('current_contact', function () { clearTimeout(curTimer); curTimer = setTimeout(reloadCurrent, SETTLE_MS); });
    es.addEventListener('station_updated', function (e) {
      var box = byId('current');
      if (box && box.dataset.call && box.dataset.call.toUpperCase() === e.data.toUpperCase()) settled(reloadCurrent);
    });
  }

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
        var c = tr.querySelector('.call');
        if (c && c.textContent.trim().toUpperCase() === call) put(tr.id.slice(4));
      });
    });
    return;
  }

  // Card views (decide / workcard) and the detail pane of the master-detail
  // pages (md-inbox / md-desk).
  var desk = mode === 'workcard' || mode === 'md-desk';
  var md = mode === 'md-inbox' || mode === 'md-desk';
  var cfg = desk
    ? { id: 'workcard', base: '/work/card', enters: 'decided', list: '/work/list' }
    : { id: 'decide', base: '/decide', enters: 'queued', list: '/queue/list' };
  // reloadCard shows the card with key (or the first one). keep=true carries
  // the operator's unsaved choices on a Desk card (route, manager, unticked
  // QSOs) through the reload.
  function reloadCard(key, keep) {
    var el = byId(cfg.id);
    var q = [];
    if (md) q.push('md=1');
    if (key) q.push('key=' + encodeURIComponent(key));
    if (el && el.dataset.filter) q.push('filter=' + encodeURIComponent(el.dataset.filter));
    if (keep && el && desk) {
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
    // source = the pane: htmx keeps its one-request-at-a-time lock on the
    // element being replaced (a body-sourced load whose target was swapped
    // away meanwhile would wedge the lock), and drops a queued load whose
    // pane is gone.
    htmx.ajax('GET', cfg.base + (q.length ? '?' + q.join('&') : ''), { source: el || document.body, target: '#' + cfg.id, swap: 'outerHTML' });
  }
  function typing(el) {
    if (!el) return false;
    if (el.querySelector('input:focus:not([type="radio"]):not([type="checkbox"]), select:focus, textarea:focus')) return true;
    var req = el.querySelector('#req');
    return !!(req && !req.hidden); // filling in a request: do not throw it away
  }
  function keysOf(el) { return (el.dataset.keys || el.dataset.qslkey || '').split(' ').filter(Boolean); }

  if (!md) {
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
        } else if (desk && d.to === cfg.enters && el.dataset.call &&
                   d.key.split('|')[0].toUpperCase() === el.dataset.call.toUpperCase()) {
          reloadCard(shown, true); // another QSO with this station joined the card
        }
      });
    });
  }
  es.addEventListener('station_updated', function (e) {
    var call = e.data.toUpperCase();
    settled(function () { // not across the operator's own action in flight
      var el = byId(cfg.id);
      if (!el || !el.dataset.qslkey) return;
      // The manager's address landed: refresh just that block (keeps focus).
      if (desk && (el.dataset.mgr || '').toUpperCase() === call && (el.dataset.call || '').toUpperCase() !== call) {
        var box = byId('mgr-addr');
        if (box) htmx.ajax('GET', '/work/manager?manager=' + encodeURIComponent(call), { source: box, target: '#mgr-addr', swap: 'innerHTML' });
        return;
      }
      if (typing(el)) return;
      if ((el.dataset.call || '').toUpperCase() === call) {
        reloadCard(el.dataset.qslkey, desk);
      }
    });
  });
  if (!md) return;

  // Master-detail: the list (#md-list) only selects; the detail pane decides.
  // After an action the server answers with the card below the handled one;
  // the list follows the detail. Live changes reload the list (keeping the
  // selection); when the shown card left (handled in another
  // window) the selection moves to the row that took its place.
  var list = byId('md-list'), box = list.closest('.md-list');
  function rows() { return Array.prototype.slice.call(list.querySelectorAll('tr.md-row')); }
  function shownKeys() { var el = byId(cfg.id); return el ? keysOf(el) : []; }
  // rowOf finds the list row of a card: Desk rows carry all keys of the card,
  // whose lead may change when a newer QSO joins it.
  function rowOf(keys) {
    return rows().filter(function (r) {
      var rk = (r.dataset.keys || r.dataset.key).split(' ');
      return keys.some(function (k) { return rk.indexOf(k) >= 0; });
    })[0] || null;
  }
  var markedKey = null; // the highlighted row
  var want = null;      // a selection whose card is still loading
  function mark(row) {
    rows().forEach(function (r) { r.classList.toggle('sel', r === row); });
    if (!row) { markedKey = null; return; }
    if (row.dataset.key === markedKey) return; // only a new selection scrolls
    markedKey = row.dataset.key;
    if (box) { // keep it in view inside the list, never scroll the window
      var b = box.getBoundingClientRect(), r = row.getBoundingClientRect();
      if (r.top < b.top) box.scrollTop -= b.top - r.top;
      else if (r.bottom > b.bottom) box.scrollTop += r.bottom - b.bottom;
    }
    history.replaceState(null, '', location.pathname + '?key=' + encodeURIComponent(row.dataset.key));
  }
  function select(row) {
    if (!row) return;
    mark(row);
    if (shownKeys().indexOf(row.dataset.key) >= 0) { want = null; return; }
    want = row.dataset.key;
    reloadCard(want, false);
  }
  mark(rowOf(shownKeys()));
  list.addEventListener('click', function (ev) {
    if (ev.target.closest('input, a, button, select, label')) return;
    select(ev.target.closest('tr.md-row'));
  });
  document.addEventListener('keydown', function (ev) {
    if (ev.metaKey || ev.ctrlKey || ev.altKey || qslKeys.editing(ev.target)) return;
    if (ev.key !== 'ArrowDown' && ev.key !== 'ArrowUp') return;
    ev.preventDefault();
    // The cursor is the highlighted row (the pane may still be loading).
    var rs = rows(), cur = rs.indexOf(list.querySelector('tr.md-row.sel') || rowOf(shownKeys()));
    var next = cur < 0 ? 0 : cur + (ev.key === 'ArrowDown' ? 1 : -1);
    if (next >= 0 && next < rs.length) select(rs[next]);
  });
  // The pane was swapped (an action moved on, a reload): follow it in the
  // list - unless a newer selection is pending, which then gets loaded (a
  // load superseded while in flight is dropped by htmx).
  document.addEventListener('htmx:afterSettle', function (ev) {
    if (!ev.target || ev.target.id !== cfg.id) return;
    var shown = shownKeys();
    if (want && shown.indexOf(want) < 0) {
      var r = rowOf([want]);
      if (r) { mark(r); reloadCard(want, false); return; }
    }
    want = null;
    mark(rowOf(shown));
  });
  // A finishing action moves to the card below the handled one: send the
  // neighbours as the list shows them now (the ones in the action URL were
  // fixed when the card was drawn). Body values win over the query string.
  document.body.addEventListener('htmx:configRequest', function (ev) {
    var elt = ev.detail.elt;
    if (ev.detail.verb === 'get' || !elt || !elt.closest || !elt.closest('#' + cfg.id)) return;
    var rs = rows(), i = rs.indexOf(rowOf(shownKeys()));
    if (i < 0) return;
    ev.detail.parameters.next = rs[i + 1] ? rs[i + 1].dataset.key : '';
    ev.detail.parameters.prev = rs[i - 1] ? rs[i - 1].dataset.key : '';
  });

  var countEl = byId('queue-count');
  var listTimer = null;
  function reloadList() {
    clearTimeout(listTimer);
    listTimer = setTimeout(function () {
      var before = rows(), at = before.indexOf(rowOf(shownKeys()));
      fetch(cfg.list).then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
        if (html === null) return;
        list.innerHTML = html;
        if (window.htmx) htmx.process(list);
        if (countEl) countEl.textContent = rows().length;
        var el = byId(cfg.id), shown = shownKeys(), row = rowOf(shown), rs = rows();
        if (row) { // still listed; a Desk card may have grown or lost QSOs
          mark(row);
          var rk = (row.dataset.keys || row.dataset.key).split(' ').filter(Boolean);
          if (desk && el && !typing(el) && rk.join(' ') !== shown.join(' ')) reloadCard(row.dataset.key, true);
          return;
        }
        if (el && typing(el)) return; // being edited: a stale action gets 409
        if (!rs.length) { if (shown.length) reloadCard('', false); return; }
        if (!shown.length && at < 0) { select(rs[0]); return; }
        select(rs[Math.min(Math.max(at, 0), rs.length - 1)]);
      });
    }, 150);
  }
  es.addEventListener('queue_changed', function () { settled(reloadList); });
  es.addEventListener('station_updated', function () { settled(reloadList); });

})();

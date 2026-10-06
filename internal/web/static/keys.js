// Keyboard handling for the card views, shared by the card-by-card pages and
// the master-detail pages: qslKeys.inbox() for the Inbox card (#decide),
// qslKeys.desk() for the Desk card (#workcard). A key acts on the control
// whose data-key matches; nothing fires while a text field has focus.
window.qslKeys = (function () {
  function editing(t) {
    return t && (t.tagName === 'INPUT' && t.type !== 'radio' && t.type !== 'checkbox' ||
      t.tagName === 'SELECT' || t.tagName === 'TEXTAREA');
  }
  function plain(ev) { return !(ev.metaKey || ev.ctrlKey || ev.altKey || ev.repeat) && !editing(ev.target); }

  // Inbox: y (or j) yes, card; n no card; left/right browse on the
  // card-by-card page. A decision shows the next card down.
  function inbox() {
    document.addEventListener('keydown', function (ev) {
      if (!plain(ev)) return;
      var k = ev.key.toLowerCase();
      if (k === 'j') k = 'y';
      var btn = document.querySelector('#decide button[data-key="' + k + '"]');
      if (!btn) return;
      ev.preventDefault();
      btn.click();
    });
  }

  // Desk: b/d/m/v pick the route (bureau, direct, via manager direct or
  // bureau), p prints, w records a hand-written card, r opens "requested"
  // (Enter in its note records it), n no card; left/right
  // browse on the card-by-card page. Each finishing action shows the next card.
  function desk() {
    function pick(r) {
      r.checked = true;
      r.dispatchEvent(new Event('change', { bubbles: true }));
      if (r.value.charAt(0) === 'M') {
        var inp = document.querySelector('#route-pick .mgr-in');
        if (inp && !inp.value.trim()) inp.focus();
      }
    }
    // The address shown follows the chosen route.
    document.addEventListener('change', function (ev) {
      var t = ev.target;
      if (t && t.name === 'route' && t.closest('#workcard')) t.closest('#workcard').dataset.route = t.value.charAt(0);
    });
    document.addEventListener('keydown', function (ev) {
      if (!plain(ev)) return;
      var k = ev.key.toLowerCase();
      var r = document.querySelector('#route-pick input[data-key="' + k + '"]');
      if (r) { ev.preventDefault(); pick(r); return; }
      var btn = document.querySelector('#workcard button[data-key="' + k + '"]');
      if (btn) { ev.preventDefault(); btn.click(); }
    });
  }

  return { inbox: inbox, desk: desk, editing: editing };
})();

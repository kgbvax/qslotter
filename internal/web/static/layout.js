// The card layout editor (Settings > Card layout, internal/web/layout.go).
// The page hands over window.qslLayout: the layout (model, the template's
// YAML structure), the field catalog and the translated texts. The script
// never sets text itself: every change posts the layout to
// /settings/cards/preview, which lays it out exactly as the printer would,
// and the card is drawn from that answer. Units are millimetres throughout:
// the SVG's viewBox is the card.
(function () {
  var D = window.qslLayout;
  var svg = document.getElementById('lay-svg');
  var pick = document.getElementById('lay-pick');
  if (!D || !D.model || !svg) {
    // A layout that does not load: only the picker and the forms.
    if (pick) pick.addEventListener('change', function () { location.href = '/settings/cards?name=' + encodeURIComponent(pick.value); });
    document.querySelectorAll('form[data-confirm]').forEach(function (f) {
      f.addEventListener('submit', function (ev) { if (!window.confirm(f.getAttribute('data-confirm'))) ev.preventDefault(); });
    });
    return;
  }
  var SVGNS = 'http://www.w3.org/2000/svg';
  var FAMILY = {
    helvetica: 'Helvetica, Arial, sans-serif',
    times: "'Times New Roman', Times, serif",
    courier: "'Courier New', Courier, monospace"
  };

  var model = D.model;
  fix();
  var saved = snapshot();
  var undoStack = [], redoStack = [], lastKey = '', lastAt = 0;
  var sel = -1;            // index of the selected field, -1 = none
  var ops = [];            // the card's elements from the last preview
  var drag = null;
  var leaving = false;     // a confirmed navigation: no second question
  var imgVer = 0;

  function $(id) { return document.getElementById(id); }
  function T(s) { return (D.strings && D.strings[s]) || s; }
  function fmt(s) {
    var args = Array.prototype.slice.call(arguments, 1);
    return s.replace(/%[sd]/g, function () { return args.length ? String(args.shift()) : ''; });
  }
  function notice(msg) { document.dispatchEvent(new CustomEvent('qslNotice', { detail: { value: msg } })); }
  function round1(v) { return Math.round(v * 10) / 10; }
  function num(v) {
    var f = parseFloat(String(v).replace(',', '.'));
    return isNaN(f) ? null : f;
  }

  // fix fills what the server leaves out of an empty layout.
  function fix() {
    model.fields = model.fields || [];
    model.rows = model.rows || { max: 0, pitch_mm: 0 };
  }
  function snapshot() { return JSON.stringify(model); }
  function dirty() { return snapshot() !== saved; }
  function isShape(f) { return f.kind === 'line' || f.kind === 'rect'; }
  function isRow(f) { return !isShape(f) && !f.text && D.row.some(function (c) { return c.name === f.name; }); }
  function label(f) {
    if (f.kind === 'line') return T('Line');
    if (f.kind === 'rect') return T('Box');
    if (f.text) return '“' + f.text + '”';
    return D.labels[f.name] || f.name || '?';
  }

  // --- talking to the server ---

  function post(url, body, json) {
    var opts = { method: 'POST', body: body, headers: {} };
    if (json) opts.headers['Content-Type'] = 'application/json';
    return fetch(url, opts).then(function (r) {
      var trig = r.headers.get('HX-Trigger');
      if (trig) {
        try { var o = JSON.parse(trig); if (o.qslNotice) notice(o.qslNotice); } catch (_) { /* not ours */ }
      }
      if (!r.ok) {
        return r.text().then(function (t) {
          notice(T('Error') + ' ' + r.status + ': ' + t.replace(/\s+/g, ' ').trim());
          throw new Error(t);
        });
      }
      return r;
    }, function (e) {
      notice(T('Network error - is the qslotter server running?'));
      throw e;
    });
  }
  function sample() { return $('lay-sample') ? $('lay-sample').value : 'long'; }

  var seq = 0, timer = null;
  function schedule() { clearTimeout(timer); timer = setTimeout(preview, 80); }
  function preview() {
    var my = ++seq;
    fetch('/settings/cards/preview?sample=' + encodeURIComponent(sample()), {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: snapshot()
    }).then(function (r) { return r.json(); }).then(function (a) {
      if (my !== seq) return;
      showError(a.error || '');
      if (a.error) return;
      ops = a.ops || [];
      showWarnings(a.warnings || []);
      draw();
    }).catch(function () {
      if (my === seq) showError(T('Network error - is the qslotter server running?'));
    });
  }
  function showError(msg) {
    var p = $('lay-error');
    p.textContent = msg;
    p.hidden = !msg;
  }
  function showWarnings(list) {
    var ul = $('lay-warn');
    ul.textContent = '';
    list.forEach(function (w) {
      var li = document.createElement('li');
      li.textContent = w;
      ul.appendChild(li);
    });
  }

  // --- changes and undo ---

  // change applies fn to the model as one undo step; changes with the same
  // key within a moment (typing a number, nudging) are one step.
  function change(fn, key) {
    var before = snapshot();
    fn();
    if (snapshot() === before) return;
    var now = Date.now();
    if (!key || key !== lastKey || now - lastAt > 1000) {
      undoStack.push(before);
      if (undoStack.length > 100) undoStack.shift();
    }
    lastKey = key || '';
    lastAt = now;
    redoStack = [];
    changed();
  }
  function changed() {
    if (sel >= model.fields.length) sel = model.fields.length - 1;
    $('lay-undo').disabled = !undoStack.length;
    $('lay-redo').disabled = !redoStack.length;
    $('lay-dirty').hidden = !dirty();
    tools();
    syncProps();
    syncCard();
    buildList();
    schedule();
  }
  function restore(from, to) {
    if (!from.length) return;
    to.push(snapshot());
    model = JSON.parse(from.pop());
    fix();
    lastKey = '';
    buildProps();
    changed();
  }
  function undo() { restore(undoStack, redoStack); }
  function redo() { restore(redoStack, undoStack); }

  // --- drawing ---

  function el(name, attrs, parent) {
    var e = document.createElementNS(SVGNS, name);
    for (var k in attrs) e.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(e);
    return e;
  }
  var view = { grid: true, margin: true, img: true, opacity: 60 };
  try { Object.assign(view, JSON.parse(localStorage.getItem('qslLayoutView') || '{}')); } catch (_) { /* defaults */ }

  function draw() {
    var W = model.width_mm || 100, H = model.height_mm || 74;
    svg.setAttribute('viewBox', [-4, -4, W + 8, H + 8].join(' '));
    svg.textContent = '';
    el('rect', { x: 0, y: 0, width: W, height: H, 'class': 'lay-paper' }, svg);
    if (D.image && view.img) {
      el('image', { href: D.image + '&v=' + imgVer, x: 0, y: 0, width: W, height: H,
        preserveAspectRatio: 'none', opacity: view.opacity / 100 }, svg);
    }
    if (view.grid) {
      var g = el('g', { 'class': 'lay-grid' }, svg);
      for (var x = 5; x < W; x += 5) el('line', { x1: x, y1: 0, x2: x, y2: H, 'class': x % 10 ? '' : 'major' }, g);
      for (var y = 5; y < H; y += 5) el('line', { x1: 0, y1: y, x2: W, y2: y, 'class': y % 10 ? '' : 'major' }, g);
    }
    if (view.margin) {
      el('rect', { x: D.margin, y: D.margin, width: Math.max(W - 2 * D.margin, 0), height: Math.max(H - 2 * D.margin, 0),
        'class': 'lay-margin' }, svg);
    }
    el('rect', { x: 0, y: 0, width: W, height: H, 'class': 'lay-edge' }, svg);
    var groups = model.fields.map(function (f, i) {
      return el('g', { 'data-field': i, 'class': 'lay-el' + (i === sel ? ' sel' : '') }, svg);
    });
    ops.forEach(function (op) { if (groups[op.field]) drawOp(groups[op.field], op); });
    model.fields.forEach(function (f, i) { if (!groups[i].firstChild) drawGhost(groups[i], f); });
    model.fields.forEach(function (f, i) {
      var b = wrapBox(f);
      if (b) el('rect', { x: b.x, y: b.y, width: b.w, height: b.h, 'class': 'lay-wrapbox' }, groups[i]);
    });
    drawSelection();
  }

  function drawOp(g, op) {
    var cls = 'lay-op' + (op.row > 0 ? ' later' : '') + (op.fitted ? ' fitted' : '') + (op.outside ? ' outside' : '');
    if (op.kind === 'text') {
      var t = el('text', {
        x: op.x, y: op.baseline, 'font-size': op.h,
        'font-family': FAMILY[(op.font || '').toLowerCase()] || FAMILY.helvetica,
        'font-weight': op.style === 'B' ? 700 : 400,
        textLength: op.w, lengthAdjust: 'spacingAndGlyphs', 'class': cls
      }, g);
      t.textContent = op.text;
      el('rect', { x: op.x, y: op.baseline - 0.75 * op.h, width: Math.max(op.w, 1), height: op.h, 'class': 'lay-hit' }, g);
    } else if (op.kind === 'line') {
      var a = { x1: op.x, y1: op.y, x2: op.x + op.w, y2: op.y + op.h };
      el('line', Object.assign({ 'stroke-width': op.stroke, 'class': cls + ' shape' }, a), g);
      el('line', Object.assign({ 'class': 'lay-hit-stroke' }, a), g);
    } else {
      var b = { x: op.x, y: op.y, width: op.w, height: op.h };
      el('rect', Object.assign({ 'stroke-width': op.stroke, 'class': cls + ' shape' }, b), g);
      el('rect', Object.assign({ 'class': 'lay-hit-stroke' }, b), g);
    }
  }

  // drawGhost marks a field that prints nothing on this sample (e.g. "via"
  // on a card without a manager), so it can still be seen and moved.
  // The address field shows an example address of four lines (line spacing
  // as in printer.addressLineFactor).
  var GHOST_ADDRESS = ['Hans Mustermann', 'Musterstrasse 12', '12345 Musterstadt', 'Germany'];
  function drawGhost(g, f) {
    var size = (f.font_size || 12) * 25.4 / 72;
    var anchor = { C: 'middle', R: 'end' }[(f.align || 'L').toUpperCase()] || 'start';
    var lines = !f.text && f.name === 'address' ? GHOST_ADDRESS : ['‹' + label(f) + '›'];
    lines.forEach(function (line, n) {
      var t = el('text', { x: f.x_mm, y: f.y_mm + n * size * 1.2 + 0.3 * size, 'font-size': size, 'text-anchor': anchor,
        'class': 'lay-ghost' }, g);
      t.textContent = line;
      var title = el('title', {}, t);
      title.textContent = T('no value on this sample');
      var bb = t.getBBox();
      el('rect', { x: bb.x, y: bb.y, width: Math.max(bb.width, 1), height: Math.max(bb.height, 1), 'class': 'lay-hit' }, g);
    });
  }

  // wrapBox is the area a text with a width may fill: its width, as many
  // lines as it may wrap onto (null for other fields).
  function wrapBox(f) {
    if (isShape(f) || !(f.w_mm > 0)) return null;
    var size = (f.font_size || 12) * 25.4 / 72, lines = f.lines > 0 ? f.lines : 2;
    var a = (f.align || 'L').toUpperCase();
    var x = a === 'C' ? f.x_mm - f.w_mm / 2 : a === 'R' ? f.x_mm - f.w_mm : f.x_mm;
    return { x: x, y: f.y_mm - 0.65 * size, w: f.w_mm, h: (lines - 1) * 1.2 * size + 1.3 * size };
  }

  function drawSelection() {
    var old = svg.querySelector('.lay-selg');
    if (old) old.remove();
    if (sel < 0) return;
    var g = svg.querySelector('[data-field="' + sel + '"]');
    if (!g || !g.firstChild) return;
    var bb = g.getBBox();
    var sg = el('g', { 'class': 'lay-selg' }, svg);
    el('rect', { x: bb.x - 0.6, y: bb.y - 0.6, width: bb.width + 1.2, height: bb.height + 1.2, 'class': 'lay-selbox' }, sg);
    var f = model.fields[sel];
    if (isShape(f)) {
      el('rect', { x: f.x_mm + (f.w_mm || 0) - 1, y: f.y_mm + (f.h_mm || 0) - 1, width: 2, height: 2, 'class': 'lay-handle' }, sg);
    }
    var wb = wrapBox(f);
    if (wb) { // drag the right edge: the text's width
      el('rect', { x: wb.x + wb.w - 1, y: wb.y + wb.h / 2 - 1, width: 2, height: 2, 'class': 'lay-handle lay-handle-w' }, sg);
    }
  }

  function select(i) {
    if (i === sel) return;
    sel = i;
    svg.querySelectorAll('.lay-el.sel').forEach(function (g) { g.classList.remove('sel'); });
    var g = svg.querySelector('[data-field="' + sel + '"]');
    if (g) g.classList.add('sel');
    drawSelection();
    buildProps();
    buildList();
    tools();
  }

  // --- dragging ---

  function pt(ev) {
    var p = svg.createSVGPoint();
    p.x = ev.clientX;
    p.y = ev.clientY;
    return p.matrixTransform(svg.getScreenCTM().inverse());
  }
  svg.addEventListener('pointerdown', function (ev) {
    if (ev.button !== 0) return;
    var handle = ev.target.closest('.lay-handle');
    var g = ev.target.closest('[data-field]');
    if (!handle && !g) { select(-1); return; }
    if (g) select(+g.getAttribute('data-field'));
    if (sel < 0) return;
    var f = model.fields[sel];
    drag = { mode: handle ? 'size' : 'move', start: pt(ev), f: f, x: f.x_mm, y: f.y_mm, w: f.w_mm || 0, h: f.h_mm || 0,
      dx: 0, dy: 0, moved: false };
    svg.setPointerCapture(ev.pointerId);
    ev.preventDefault();
  });
  svg.addEventListener('pointermove', function (ev) {
    if (!drag) return;
    var p = pt(ev);
    var dx = p.x - drag.start.x, dy = p.y - drag.start.y;
    if (!drag.moved && Math.abs(dx) < 0.3 && Math.abs(dy) < 0.3) return;
    drag.moved = true;
    if (ev.shiftKey) { if (Math.abs(dx) > Math.abs(dy)) dy = 0; else dx = 0; }
    drag.dx = round1(dx);
    drag.dy = round1(dy);
    var g = svg.querySelector('[data-field="' + sel + '"]');
    var sg = svg.querySelector('.lay-selg');
    if (drag.mode === 'move') {
      var tr = 'translate(' + drag.dx + ' ' + drag.dy + ')';
      if (g) g.setAttribute('transform', tr);
      if (sg) sg.setAttribute('transform', tr);
      showPos(drag.x + drag.dx, drag.y + drag.dy);
    } else if (!isShape(drag.f)) { // a text's width
      var tw = Math.max(5, round1(drag.w + drag.dx));
      var box = g && g.querySelector('.lay-wrapbox');
      var wb0 = wrapBox(drag.f);
      if (box && wb0) box.setAttribute('width', tw);
      var hw = sg && sg.querySelector('.lay-handle');
      if (hw && wb0) hw.setAttribute('x', wb0.x + tw - 1);
      var iw = document.querySelector('#lay-props [data-prop="w_mm"]');
      if (iw) iw.value = tw;
    } else {
      var w = Math.max(0, round1(drag.w + drag.dx)), h = Math.max(0, round1(drag.h + drag.dy));
      if (g) {
        g.querySelectorAll(drag.f.kind === 'line' ? 'line' : 'rect').forEach(function (s) {
          if (drag.f.kind === 'line') { s.setAttribute('x2', drag.x + w); s.setAttribute('y2', drag.y + h); }
          else { s.setAttribute('width', w); s.setAttribute('height', h); }
        });
      }
      var hd = sg && sg.querySelector('.lay-handle');
      if (hd) { hd.setAttribute('x', drag.x + w - 1); hd.setAttribute('y', drag.y + h - 1); }
    }
  });
  function endDrag() {
    if (!drag) return;
    var d = drag;
    drag = null;
    if (!d.moved) return;
    change(function () {
      if (d.mode === 'move') {
        d.f.x_mm = round1(d.x + d.dx);
        d.f.y_mm = round1(d.y + d.dy);
      } else if (!isShape(d.f)) {
        d.f.w_mm = Math.max(5, round1(d.w + d.dx));
      } else {
        d.f.w_mm = Math.max(0, round1(d.w + d.dx));
        d.f.h_mm = Math.max(0, round1(d.h + d.dy));
        if (!d.f.w_mm && !d.f.h_mm) d.f.w_mm = 1;
      }
    });
  }
  svg.addEventListener('pointerup', endDrag);
  svg.addEventListener('pointercancel', endDrag);
  function showPos(x, y) {
    var ix = document.querySelector('#lay-props [data-prop="x_mm"]');
    var iy = document.querySelector('#lay-props [data-prop="y_mm"]');
    if (ix) ix.value = round1(x);
    if (iy) iy.value = round1(y);
  }

  // --- adding, duplicating, deleting ---

  function add(what) {
    var H = model.height_mm || 74, n = model.fields.length;
    var y = round1(Math.min(H - 6, 10 + (n % 8) * 6));
    var f;
    if (what === 'line') f = { kind: 'line', x_mm: 10, y_mm: y, w_mm: 40, h_mm: 0, stroke_mm: 0.3 };
    else if (what === 'rect') f = { kind: 'rect', x_mm: 10, y_mm: y, w_mm: 30, h_mm: 12, stroke_mm: 0.3 };
    else if (what === 'text') f = { name: 'text', text: T('Text'), x_mm: 10, y_mm: y, font_size: 10, align: 'L', font: 'Helvetica' };
    else f = { name: what.replace(/^field:/, ''), x_mm: 10, y_mm: y, font_size: 11, align: 'L', font: 'Helvetica' };
    change(function () { model.fields.push(f); });
    select(model.fields.length - 1);
  }
  function duplicate() {
    if (sel < 0) return;
    var f = JSON.parse(JSON.stringify(model.fields[sel]));
    f.x_mm = round1(f.x_mm + 2);
    f.y_mm = round1(f.y_mm + 2);
    change(function () { model.fields.splice(sel + 1, 0, f); });
    select(sel + 1);
  }
  function remove() {
    if (sel < 0) return;
    var i = sel;
    change(function () { model.fields.splice(i, 1); });
    sel = -1;
    select(Math.min(i, model.fields.length - 1));
    buildProps();
  }

  // --- cut, copy, paste (the clipboard is kept in localStorage, so an
  // element can be pasted into another layout) ---

  var CLIP = 'qslLayoutClip';
  function clip() {
    try { return JSON.parse(localStorage.getItem(CLIP) || 'null'); } catch (_) { return null; }
  }
  function copySel() {
    if (sel < 0) return;
    try { localStorage.setItem(CLIP, JSON.stringify(model.fields[sel])); } catch (_) { /* private mode */ }
    tools();
  }
  function cutSel() {
    if (sel < 0) return;
    copySel();
    remove();
    select(-1);
  }
  // paste puts the element back where it was; on top of an equal one it
  // moves 2 mm down and right, so the copy can be seen and grabbed.
  function paste() {
    var f = clip();
    if (!f) return;
    function taken(g) {
      return model.fields.some(function (h) {
        return h.kind === g.kind && h.name === g.name && h.text === g.text &&
          Math.abs(h.x_mm - g.x_mm) < 0.05 && Math.abs(h.y_mm - g.y_mm) < 0.05;
      });
    }
    for (var n = 0; n < 50 && taken(f); n++) { f.x_mm = round1(f.x_mm + 2); f.y_mm = round1(f.y_mm + 2); }
    var at = sel < 0 ? model.fields.length : sel + 1;
    change(function () { model.fields.splice(at, 0, f); });
    select(at);
  }
  function tools() {
    $('lay-cut').disabled = $('lay-copyel').disabled = sel < 0;
    $('lay-paste').disabled = !clip();
  }
  $('lay-cut').addEventListener('click', cutSel);
  $('lay-copyel').addEventListener('click', copySel);
  $('lay-paste').addEventListener('click', paste);
  window.addEventListener('storage', function (ev) { if (ev.key === CLIP) tools(); });

  // --- the properties of the selected element ---

  function field(labelText, input) {
    var l = document.createElement('label');
    l.appendChild(document.createTextNode(labelText + ' '));
    l.appendChild(input);
    return l;
  }
  function numInput(prop, step, min, max) {
    var i = document.createElement('input');
    i.type = 'number';
    i.step = step;
    if (min != null) i.min = min;
    if (max != null) i.max = max;
    i.setAttribute('data-prop', prop);
    i.addEventListener('input', function () {
      var v = num(i.value);
      if (v === null || sel < 0) return;
      if (prop === 'lines') v = Math.max(1, Math.round(v));
      var f = model.fields[sel];
      change(function () { f[prop] = v; }, 'prop' + sel + prop);
    });
    return i;
  }
  function selectInput(prop, options, onSet) {
    var s = document.createElement('select');
    s.setAttribute('data-prop', prop);
    options.forEach(function (o) {
      if (o.group) {
        var og = document.createElement('optgroup');
        og.label = o.group;
        o.items.forEach(function (it) { og.appendChild(new Option(it[1], it[0])); });
        s.appendChild(og);
      } else {
        s.appendChild(new Option(o[1], o[0]));
      }
    });
    s.addEventListener('change', function () {
      if (sel < 0) return;
      var f = model.fields[sel];
      change(function () { onSet(f, s.value); });
    });
    return s;
  }
  function source(f) { return f.text || !D.labels[f.name] ? 'text' : f.name; }

  function buildProps() {
    var box = $('lay-props');
    box.textContent = '';
    if (sel < 0 || !model.fields[sel]) {
      var p = document.createElement('p');
      p.className = 'muted';
      p.textContent = T('Click an element on the card, or pick one here.');
      box.appendChild(p);
      return;
    }
    var f = model.fields[sel];
    var h = document.createElement('h4');
    h.textContent = label(f) + (isRow(f) ? ' · ' + T('per QSO') : '');
    box.appendChild(h);
    if (!isShape(f)) {
      var opts = [['text', T('Fixed text')],
        { group: '—', items: D.card.map(function (c) { return [c.name, c.label]; }) },
        { group: T('per QSO'), items: D.row.map(function (c) { return [c.name, c.label]; }) }];
      if (f.name === 'qslmsg') opts.push([f.name, D.labels.qslmsg]);
      box.appendChild(field(T('Data field'), selectInput('source', opts, function (f, v) {
        if (v === 'text') { f.name = 'text'; f.text = f.text || label(f); }
        else { f.name = v; delete f.text; }
        setTimeout(function () { buildProps(); var s = box.querySelector('[data-prop="source"]'); if (s) s.focus(); });
      })));
      if (source(f) === 'text') {
        var ti = document.createElement('input');
        ti.type = 'text';
        ti.setAttribute('data-prop', 'text');
        ti.addEventListener('input', function () {
          var ff = model.fields[sel];
          change(function () { ff.text = ti.value; }, 'prop' + sel + 'text');
        });
        box.appendChild(field(T('Text'), ti));
      }
    }
    var pos = document.createElement('div');
    pos.className = 'lay-grid2';
    pos.appendChild(field(T('X (mm)'), numInput('x_mm', 0.1)));
    pos.appendChild(field(T('Y (mm)'), numInput('y_mm', 0.1)));
    if (isShape(f)) {
      pos.appendChild(field(T('Width (mm)'), numInput('w_mm', 0.1, 0)));
      pos.appendChild(field(T('Height (mm)'), numInput('h_mm', 0.1, 0)));
      pos.appendChild(field(T('Line width (mm)'), numInput('stroke_mm', 0.05, 0.05, 10)));
    } else {
      pos.appendChild(field(T('Size (pt)'), numInput('font_size', 0.5, 4, 72)));
      pos.appendChild(field(T('Width (mm)'), numInput('w_mm', 0.5, 0, 1000)));
      pos.appendChild(field(T('Lines'), numInput('lines', 1, 1, 20)));
      pos.appendChild(field(T('Font'), selectInput('font', D.fonts.map(function (n) { return [n, n]; }),
        function (f, v) { f.font = v; })));
      pos.appendChild(field(T('Align'), selectInput('align', [['L', T('left')], ['C', T('centre')], ['R', T('right')]],
        function (f, v) { f.align = v; })));
      var b = document.createElement('input');
      b.type = 'checkbox';
      b.setAttribute('data-prop', 'style');
      b.addEventListener('change', function () {
        var ff = model.fields[sel];
        change(function () { if (b.checked) ff.style = 'B'; else delete ff.style; });
      });
      var bl = document.createElement('label');
      bl.className = 'lay-check';
      bl.appendChild(b);
      bl.appendChild(document.createTextNode(' ' + T('Bold')));
      pos.appendChild(bl);
    }
    box.appendChild(pos);
    var w = document.createElement('input');
    w.type = 'checkbox';
    w.setAttribute('data-prop', 'when');
    w.addEventListener('change', function () {
      var ff = model.fields[sel];
      change(function () { if (w.checked) ff.when = 'sat'; else delete ff.when; });
    });
    var wl = document.createElement('label');
    wl.className = 'lay-check';
    wl.title = T('For a satellite column and its heading: an HF card leaves them out.');
    wl.appendChild(w);
    wl.appendChild(document.createTextNode(' ' + T('Only on satellite cards')));
    box.appendChild(wl);
    if (!isShape(f)) {
      var hint = document.createElement('p');
      hint.className = 'muted lay-hint';
      hint.textContent = T('X is where the text starts (left), its middle (centre) or where it ends (right).') + ' ' +
        T('Width 0: one line up to the card margin. With a width, a longer text wraps onto up to Lines lines, then gets smaller.');
      box.appendChild(hint);
    }
    var acts = document.createElement('p');
    var dup = document.createElement('button');
    dup.type = 'button';
    dup.className = 'mini';
    dup.textContent = T('Duplicate');
    dup.addEventListener('click', duplicate);
    var del = document.createElement('button');
    del.type = 'button';
    del.className = 'mini danger';
    del.textContent = T('Delete');
    del.addEventListener('click', remove);
    acts.appendChild(dup);
    acts.appendChild(document.createTextNode(' '));
    acts.appendChild(del);
    box.appendChild(acts);
    syncProps();
  }

  // syncProps shows the model's values in the property inputs, except in
  // the one being typed in.
  function syncProps() {
    if (sel < 0) return;
    var f = model.fields[sel];
    if (!f) return;
    $('lay-props').querySelectorAll('[data-prop]').forEach(function (i) {
      if (i === document.activeElement && i.tagName === 'INPUT' && i.type !== 'checkbox') return;
      var p = i.getAttribute('data-prop');
      if (p === 'source') i.value = source(f);
      else if (p === 'style') i.checked = (f.style || '').toUpperCase() === 'B';
      else if (p === 'when') i.checked = (f.when || '').toLowerCase() === 'sat';
      else if (p === 'stroke_mm') i.value = f.stroke_mm || 0.3;
      else if (p === 'align') i.value = (f.align || 'L').toUpperCase();
      else if (p === 'font') i.value = D.fonts.filter(function (n) { return n.toLowerCase() === (f.font || 'helvetica').toLowerCase(); })[0] || 'Helvetica';
      else i.value = f[p] == null ? '' : f[p];
    });
  }

  function buildList() {
    var ol = $('lay-list');
    ol.textContent = '';
    model.fields.forEach(function (f, i) {
      var li = document.createElement('li');
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'lay-item' + (i === sel ? ' sel' : '');
      b.textContent = label(f);
      if (isRow(f)) {
        var s = document.createElement('span');
        s.className = 'muted';
        s.textContent = ' · ' + T('per QSO');
        b.appendChild(s);
      }
      if ((f.when || '').toLowerCase() === 'sat') {
        var sw = document.createElement('span');
        sw.className = 'muted';
        sw.textContent = ' · ' + T('satellite cards');
        b.appendChild(sw);
      }
      b.addEventListener('click', function () { select(i); });
      li.appendChild(b);
      ol.appendChild(li);
    });
  }

  // --- the card settings ---

  var cardInputs = { 'lay-w': ['width_mm'], 'lay-h': ['height_mm'], 'lay-rows': ['rows', 'max'], 'lay-pitch': ['rows', 'pitch_mm'] };
  Object.keys(cardInputs).forEach(function (id) {
    var path = cardInputs[id];
    $(id).addEventListener('input', function () {
      var v = num($(id).value);
      if (v === null) return;
      change(function () {
        if (path.length === 1) model[path[0]] = v;
        else model[path[0]][path[1]] = path[1] === 'max' ? Math.round(v) : v;
      }, id);
    });
  });
  function syncCard() {
    Object.keys(cardInputs).forEach(function (id) {
      var path = cardInputs[id], i = $(id);
      if (i === document.activeElement) return;
      i.value = path.length === 1 ? model[path[0]] : (model[path[0]][path[1]] || (path[1] === 'max' ? 1 : 0));
    });
    var paper = $('lay-paper');
    var off = D.paper && D.paper[0] && (Math.abs(D.paper[0] - model.width_mm) > 0.01 || Math.abs(D.paper[1] - model.height_mm) > 0.01);
    paper.hidden = !off;
    if (off) {
      paper.textContent = fmt(T('The printer is set up for %s x %s mm paper (printer.paper_size_mm in the config file), this card is %s x %s mm.'),
        D.paper[0], D.paper[1], model.width_mm, model.height_mm);
    }
  }

  // --- toolbar ---

  $('lay-add').addEventListener('change', function () {
    var v = this.value;
    this.value = '';
    if (v) add(v);
  });
  $('lay-undo').addEventListener('click', undo);
  $('lay-redo').addEventListener('click', redo);
  // ownLayout makes the layout on screen the operator's own: the built-in
  // layout (or a file outside cards/) is saved as a new layout, which
  // prints the cards when this one did. It resolves to {name, location}.
  function ownLayout() {
    return post('/settings/cards/save?create=1&auto=1&from=' + encodeURIComponent(D.id), snapshot(), true)
      .then(function (r) { return r.json(); });
  }
  function go(url) { leaving = true; location.href = url; }
  function save() {
    if (!$('lay-save')) return;
    var body = snapshot();
    if (!D.editable) {
      ownLayout().then(function (a) { go(a.location); }, function () { /* toast shown */ });
      return;
    }
    post('/settings/cards/save?name=' + encodeURIComponent(D.id), body, true).then(function () {
      saved = body;
      $('lay-dirty').hidden = !dirty();
    }, function () { /* toast shown */ });
  }
  if ($('lay-save')) $('lay-save').addEventListener('click', save);
  if ($('lay-copy')) {
    $('lay-copy').addEventListener('click', function () {
      var name = $('lay-copy-name').value.trim();
      if (!name) { $('lay-copy-name').focus(); return; }
      post('/settings/cards/save?create=1&name=' + encodeURIComponent(name) + '&from=' + encodeURIComponent(D.id), snapshot(), true)
        .then(function (r) { return r.json(); })
        .then(function (a) { go(a.location); }, function () { /* toast shown */ });
    });
  }
  $('lay-test').addEventListener('click', function () {
    var b = this;
    b.disabled = true;
    post('/settings/cards/test?name=' + encodeURIComponent(D.id) + '&sample=' + encodeURIComponent(sample()), snapshot(), true)
      .catch(function () { /* toast shown */ })
      .then(function () { b.disabled = false; });
  });
  var pdfURL = null;
  $('lay-pdf').addEventListener('click', function () {
    post('/settings/cards/pdf?sample=' + encodeURIComponent(sample()), snapshot(), true)
      .then(function (r) { return r.blob(); })
      .then(function (blob) {
        if (pdfURL) URL.revokeObjectURL(pdfURL);
        pdfURL = URL.createObjectURL(blob);
        var v = $('lay-pdfview');
        v.querySelector('iframe').src = pdfURL + '#view=Fit&navpanes=0';
        v.hidden = false;
      }, function () { /* toast shown */ });
  });
  $('lay-pdf-close').addEventListener('click', function () { $('lay-pdfview').hidden = true; });

  // --- the card picture ---

  function setImage(url) {
    D.image = url;
    imgVer++;
    $('lay-imgctl').hidden = !url;
    if ($('lay-img-del')) $('lay-img-del').hidden = !url;
    draw();
  }
  if ($('lay-file')) {
    $('lay-file').addEventListener('change', function () {
      var input = this;
      if (!input.files.length) return;
      var fd = new FormData();
      fd.append('image', input.files[0]);
      function upload(name) {
        return post('/settings/cards/image?name=' + encodeURIComponent(name), fd).then(function (r) { return r.json(); });
      }
      if (!D.editable) {
        // The built-in layout keeps no picture: the operator's own copy does.
        ownLayout().then(function (a) {
          return upload(a.name).then(function () { go(a.location); });
        }).catch(function () { input.value = ''; });
        return;
      }
      upload(D.id)
        .then(function (a) { setImage(a.image); }, function () { /* toast shown */ })
        .then(function () { input.value = ''; });
    });
  }
  if ($('lay-img-del')) {
    $('lay-img-del').addEventListener('click', function () {
      post('/settings/cards/image/delete?name=' + encodeURIComponent(D.id), null)
        .then(function () { setImage(''); }, function () { /* toast shown */ });
    });
  }

  // --- view options ---

  function storeView() { try { localStorage.setItem('qslLayoutView', JSON.stringify(view)); } catch (_) { /* private mode */ } }
  [['lay-grid', 'grid'], ['lay-margin', 'margin'], ['lay-img', 'img']].forEach(function (p) {
    var i = $(p[0]);
    i.checked = view[p[1]];
    i.addEventListener('change', function () { view[p[1]] = i.checked; storeView(); draw(); });
  });
  $('lay-opacity').value = view.opacity;
  $('lay-opacity').addEventListener('input', function () {
    view.opacity = +this.value;
    var img = svg.querySelector('image');
    if (img) img.setAttribute('opacity', view.opacity / 100);
  });
  $('lay-opacity').addEventListener('change', storeView);
  $('lay-sample').addEventListener('change', schedule);

  // --- keys ---

  document.addEventListener('keydown', function (ev) {
    var t = ev.target;
    var typing = t && (t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' ||
      (t.tagName === 'INPUT' && t.type !== 'checkbox' && t.type !== 'range' && t.type !== 'file'));
    var mod = ev.metaKey || ev.ctrlKey;
    var k = ev.key;
    if (mod && k.toLowerCase() === 's') { ev.preventDefault(); save(); return; }
    if (typing) return;
    if (mod && k.toLowerCase() === 'z') { ev.preventDefault(); if (ev.shiftKey) redo(); else undo(); return; }
    if (mod && k.toLowerCase() === 'y') { ev.preventDefault(); redo(); return; }
    if (mod && k.toLowerCase() === 'd') { ev.preventDefault(); duplicate(); return; }
    if (mod && k.toLowerCase() === 'x') { ev.preventDefault(); cutSel(); return; }
    if (mod && k.toLowerCase() === 'c') { ev.preventDefault(); copySel(); return; }
    if (mod && k.toLowerCase() === 'v') { ev.preventDefault(); paste(); return; }
    if (mod || sel < 0) return;
    if (k === 'Escape') { select(-1); return; }
    if (k === 'Delete' || k === 'Backspace') { ev.preventDefault(); remove(); return; }
    var dir = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[k];
    if (!dir) return;
    ev.preventDefault();
    var step = ev.shiftKey ? 5 : ev.altKey ? 0.1 : 0.5;
    var f = model.fields[sel];
    change(function () {
      f.x_mm = round1(f.x_mm + dir[0] * step);
      f.y_mm = round1(f.y_mm + dir[1] * step);
    }, 'nudge' + sel);
  });

  // --- leaving with unsaved changes ---

  function mayLeave() { return leaving || !dirty() || window.confirm(T('Discard the unsaved changes?')); }
  pick.addEventListener('change', function () {
    if (!mayLeave()) { this.value = D.id; return; }
    leaving = true;
    location.href = '/settings/cards?name=' + encodeURIComponent(this.value);
  });
  document.querySelectorAll('form.lay-form').forEach(function (f) {
    f.addEventListener('submit', function (ev) {
      var q = f.getAttribute('data-confirm');
      if ((q && !window.confirm(q)) || !mayLeave()) { ev.preventDefault(); return; }
      leaving = true;
    });
  });
  window.addEventListener('beforeunload', function (ev) {
    if (leaving || !dirty()) return;
    ev.preventDefault();
    ev.returnValue = '';
  });

  // The page's state for tests.
  window.qslLayoutState = function () { return { model: model, sel: sel, ops: ops, dirty: dirty() }; };

  svg.setAttribute('viewBox', [-4, -4, (model.width_mm || 100) + 8, (model.height_mm || 74) + 8].join(' '));
  buildProps();
  changed();
})();

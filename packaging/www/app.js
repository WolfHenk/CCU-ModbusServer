/*
 * -----------------------------------------------------------------------------
 * Projekt:      CCU-ModbusServer
 * Datei:        packaging/www/app.js
 * Ersteller:    Wolfram Henkel
 * Datum:        11. Nov. 2026
 * Version:      0.4.0
 *
 * Copyright (c) 2026 Wolfram Henkel.
 *
 * Lizenz:       CCU-ModbusServer-Lizenz für kostenfreie Nutzung und
 *               Weitergabe, Version 1.1.
 *
 * Private und gewerbliche Nutzung sind erlaubt.
 * Die Software und Änderungen dürfen nur kostenlos unter
 * derselben Lizenz und mit vollständigem Quellcode weitergegeben werden.
 *
 * Vollständiger Lizenztext: LICENSE
 * -----------------------------------------------------------------------------
 */
'use strict';

// All texts from the CCU are inserted with textContent, never as HTML.
const B = document.body, SID = B.dataset.sid, PORT = B.dataset.port;
const $ = id => document.getElementById(id);
const OFFERED = new Set(['BOOL', 'ACTION', 'FLOAT', 'INTEGER', 'ENUM']);
const TYPE_TEXT = {BOOL: 'Logik', ACTION: 'Tastendruck', FLOAT: 'Zahl', INTEGER: 'Ganzzahl', ENUM: 'Werteliste', STRING: 'Zeichenkette', OTHER: 'sonstiges'};
const FORMATS = {
  BOOL: ['bit'], ACTION: ['bit'], ENUM: ['uint16'],
  INTEGER: ['int32', 'uint32', 'int16', 'uint16', 'float32'],
  FLOAT: ['float32', 'int16', 'uint16', 'int32', 'uint32'],
};
const FORMAT_TEXT = {bit: 'Bit', int16: 'INT16 (1 Register)', uint16: 'UINT16 (1 Register)', int32: 'INT32 (2 Register)', uint32: 'UINT32 (2 Register)', float32: 'FLOAT32 (2 Register)'};
// Channel parameters that are service information, shown folded away.
const SERVICE = new Set(['UNREACH', 'STICKY_UNREACH', 'CONFIG_PENDING', 'LOWBAT', 'LOW_BAT', 'RSSI_DEVICE', 'RSSI_PEER', 'DUTY_CYCLE', 'DUTYCYCLE', 'ERROR_CODE', 'OPERATING_VOLTAGE', 'OPERATING_VOLTAGE_STATUS', 'UPDATE_PENDING', 'DEVICE_IN_BOOTLOADER', 'SABOTAGE', 'ERROR_OVERHEAT', 'ERROR_OVERLOAD', 'ERROR_UNDERVOLTAGE', 'ACTUAL_TEMPERATURE_STATUS', 'INHIBIT', 'AES_KEY', 'INSTALL_TEST', 'WORKING']);

let cat = null, map = null, sheet = [], cfg = null, keyIndex = new Map(), selected = new Map();
let busy = false;

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (k in e && typeof v !== 'string') e[k] = v;
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat()) if (c !== null && c !== undefined && c !== false) e.append(c instanceof Node ? c : String(c));
  return e;
}

async function api(path, body) {
  const opt = body === undefined ? {cache: 'no-store'} : {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)};
  const r = await fetch('api.cgi?sid=' + encodeURIComponent(SID) + '&path=' + path, opt);
  const t = await r.text();
  let j = null;
  try { j = JSON.parse(t); } catch (e) { /* not JSON */ }
  if (!r.ok) throw new Error((j && j.error) || t.trim() || ('Fehler ' + r.status));
  return j;
}

function notice(text, error) {
  const n = $('notice');
  n.textContent = text || '';
  n.className = 'notice noprint' + (text ? '' : ' hidden') + (error ? ' error' : '');
  if (text && !error) setTimeout(() => { if (n.textContent === text) notice(''); }, 4000);
}

function deNum(s) { return String(s).replace('.', ','); }

function showValue(v, type, list, unit) {
  if (v === '' || v === undefined || v === null) return '–';
  if (type === 'BOOL') return v === 'true' || v === '1' ? 'wahr' : 'falsch';
  if (type === 'ENUM' && list && list[+v] !== undefined) return list[+v] + ' (' + v + ')';
  if (type === 'FLOAT') { const f = parseFloat(v); if (isFinite(f)) v = String(+f.toFixed(3)); }
  return deNum(v) + (unit ? ' ' + unit : '');
}

function accessText(p) {
  if (p.type === 'ACTION') return 'Tastendruck (schreiben)';
  return p.writable ? 'lesen/schreiben' : 'lesen';
}

// ---------------------------------------------------------------- data

function indexCatalog() {
  keyIndex = new Map();
  const rooms = new Set(), funcs = new Set(), types = new Set();
  for (const d of cat.devices || []) {
    types.add(d.type);
    for (const ch of d.channels || []) {
      (ch.rooms || []).forEach(r => rooms.add(r));
      (ch.functions || []).forEach(f => funcs.add(f));
      for (const p of ch.points || []) keyIndex.set('dp:' + p.id, {kind: 'dp', p, ch, d});
    }
  }
  for (const v of cat.sysvars || []) keyIndex.set('sv:' + v.rega_id, {kind: 'sv', p: v});
  fillSelect('fRoom', rooms);
  fillSelect('fFunc', funcs);
  fillSelect('fType', types);
}

function fillSelect(id, values) {
  const s = $(id), keep = s.value;
  while (s.options.length > 1) s.remove(1);
  [...values].filter(Boolean).sort((a, b) => a.localeCompare(b, 'de')).forEach(v => s.add(new Option(v, v)));
  s.value = keep;
}

function indexMapping() {
  selected = new Map();
  for (const e of map.entries || []) if (!e.removed) selected.set(e.key, e);
  $('wordOrder').value = map.word_order || 'ABCD';
}

async function loadAll() {
  const [c, m, s, st, cf] = await Promise.all([api('catalog'), api('mapping'), api('sheet'), api('status'), api('config').catch(() => null)]);
  cat = c; map = m; sheet = s.rows || []; cfg = cf;
  indexCatalog();
  indexMapping();
  showStatus(st);
  if (cfg) $('allowList').value = (cfg.write_allow || []).join('\n');
  renderAll();
}

async function reloadMapping() {
  const [m, s] = await Promise.all([api('mapping'), api('sheet')]);
  map = m; sheet = s.rows || [];
  indexMapping();
  renderAll();
}

function showStatus(st) {
  if (!st) return;
  const n = st.exports || 0;
  $('exportCount').textContent = n + (n === 1 ? ' Wert freigegeben' : ' Werte freigegeben');
  const c = $('ccuState');
  c.textContent = st.ccu_ok ? 'CCU-Logik erreichbar' : 'CCU-Logik antwortet nicht' + (st.catalog_error ? ' (' + st.catalog_error + ')' : '');
  c.className = st.ccu_ok ? 'ok' : 'bad';
}

function renderAll() {
  renderDevices();
  renderSysvars();
  renderExports();
  renderSheet();
}

// selection change from a checkbox: applied immediately
async function toggle(key, on, box) {
  if (busy) { box.checked = !on; return; }
  busy = true;
  box.disabled = true;
  try {
    const info = keyIndex.get(key);
    // a key press only makes sense written: ACTION is released writable
    const add = {action: 'add', key};
    if (info && info.p.type === 'ACTION') add.writable = true;
    await api('mapping', on ? add : {action: 'remove', key});
    await reloadMapping();
    notice(on ? 'Freigegeben.' : 'Freigabe entfernt. Die Adresse bleibt reserviert.');
  } catch (e) {
    box.checked = !on;
    notice('Nicht übernommen: ' + e.message, true);
  } finally {
    busy = false;
    box.disabled = false;
  }
}

function selectable(p) {
  if (p.type === 'ACTION') return !!p.writable;
  return OFFERED.has(p.type) && p.readable !== false;
}

function pointRow(key, p, name, unit) {
  const ok = selectable(p);
  const box = el('input', {type: 'checkbox', checked: selected.has(key), disabled: !ok, 'aria-label': name});
  box.addEventListener('change', () => toggle(key, box.checked, box));
  const e = selected.get(key);
  return el('label', {class: 'point' + (ok ? '' : ' off')},
    box,
    el('span', {class: 'pname'}, name),
    el('span', {class: 'ptype'}, ok ? (TYPE_TEXT[p.type] || p.type) + ' · ' + accessText(p) : (TYPE_TEXT[p.type] || p.type) + ' – nicht angeboten'),
    el('span', {class: 'pval'}, p.type === 'ACTION' ? '' : showValue(p.value, p.type, p.value_list, unit)),
    el('span', {class: 'paddr'}, e ? addrText(e) : ''));
}

function addrText(e) {
  const row = sheet.find(r => r.key === e.key);
  return row ? row.table + ' ' + row.address : '';
}

// ---------------------------------------------------------------- devices

const openDevices = new Set();

function chanMatches(ch, room, func) {
  return (!room || (ch.rooms || []).includes(room)) && (!func || (ch.functions || []).includes(func));
}

function renderDevices() {
  const list = $('deviceList');
  if (!cat) return;
  const q = $('fName').value.trim().toLowerCase(), room = $('fRoom').value, func = $('fFunc').value, type = $('fType').value, onlySel = $('fSel').checked;
  const out = [];
  for (const d of cat.devices || []) {
    if (type && d.type !== type) continue;
    const devText = (d.name + ' ' + d.address + ' ' + d.type).toLowerCase();
    const chans = (d.channels || []).filter(ch => {
      if (!chanMatches(ch, room, func) && !(ch.address.endsWith(':0') && !room && !func)) return false;
      if (q && !devText.includes(q) && !(ch.name + ' ' + ch.address).toLowerCase().includes(q)) return false;
      if (onlySel && !(ch.points || []).some(p => selected.has('dp:' + p.id))) return false;
      return true;
    });
    if (!chans.length) continue;
    if ((room || func) && chans.every(ch => ch.address.endsWith(':0'))) continue;
    out.push(deviceCard(d, chans));
  }
  list.replaceChildren(...(out.length ? out : [el('div', {class: 'empty'}, cat.devices && cat.devices.length ? 'Keine Geräte für diesen Filter.' : 'Keine Geräte in der CCU gefunden.')]));
}

function deviceCard(d, chans) {
  const n = chans.reduce((a, ch) => a + (ch.points || []).filter(p => selected.has('dp:' + p.id)).length, 0);
  const det = el('details', {class: 'card device', open: openDevices.has(d.address)});
  const sum = el('summary', {},
    el('span', {class: 'dname'}, d.name),
    el('span', {class: 'meta'}, d.type + ' · ' + d.address + ' · ' + d.interface),
    n ? el('span', {class: 'badge'}, n + ' freigegeben') : null);
  det.append(sum);
  const body = el('div', {class: 'channels'});
  det.append(body);
  const fill = () => { if (!body.childElementCount) body.append(...channelBlocks(chans)); };
  if (det.open) fill();
  det.addEventListener('toggle', () => {
    if (det.open) { openDevices.add(d.address); fill(); } else openDevices.delete(d.address);
  });
  return det;
}

function channelBlocks(chans) {
  const out = [];
  const service = [];
  for (const ch of chans) {
    const pts = (ch.points || []).slice().sort((a, b) => a.param.localeCompare(b.param));
    const main = pts.filter(p => !SERVICE.has(p.param) && !ch.address.endsWith(':0'));
    const svc = pts.filter(p => SERVICE.has(p.param) || ch.address.endsWith(':0'));
    const unitOf = p => p.unit || '';
    if (main.length) {
      out.push(el('div', {class: 'channel'},
        el('div', {class: 'chead'}, el('strong', {}, ch.name), ' ', el('span', {class: 'meta'}, [ch.address, (ch.rooms || []).join(', '), (ch.functions || []).join(', ')].filter(Boolean).join(' · '))),
        main.map(p => pointRow('dp:' + p.id, p, p.param, unitOf(p)))));
    }
    svc.forEach(p => service.push(pointRow('dp:' + p.id, p, p.param + ' (' + ch.address + ')', unitOf(p))));
  }
  if (service.length) {
    const sel = service.some(r => r.querySelector('input').checked);
    out.push(el('details', {class: 'service', open: sel}, el('summary', {}, 'Servicewerte (' + service.length + ')'), service));
  }
  return out;
}

// ---------------------------------------------------------------- system variables

function renderSysvars() {
  if (!cat) return;
  const q = $('svName').value.trim().toLowerCase(), internal = $('svInternal').checked;
  const rows = (cat.sysvars || [])
    .filter(v => (internal || !v.internal || selected.has('sv:' + v.rega_id)) && (!q || v.name.toLowerCase().includes(q)))
    .sort((a, b) => a.name.localeCompare(b.name, 'de'))
    .map(v => pointRow('sv:' + v.rega_id, Object.assign({readable: true}, v), v.name, v.unit));
  $('sysvarList').replaceChildren(...(rows.length ? rows : [el('div', {class: 'empty'}, 'Keine Systemvariablen für diesen Filter.')]));
}

// ---------------------------------------------------------------- exports

function stateCell(r) {
  if (r.reserved) return el('span', {class: 'meta'}, 'abgewählt – Adresse bleibt reserviert');
  if (r.orphan) return el('span', {class: 'bad'}, 'nicht mehr in der CCU');
  const t = [el('span', {class: r.valid ? 'ok' : 'bad'}, r.reason)];
  if (r.comm && r.comm !== 'keine Angabe' && r.comm !== 'ok') t.push(el('div', {class: 'warn'}, r.comm));
  if (r.reception && r.reception.overdue) t.push(el('div', {class: 'warn'}, 'Meldung überfällig'));
  return el('span', {}, t);
}

function renderExports() {
  const rows = sheet.slice().sort((a, b) => a.export - b.export).map(r => {
    const info = keyIndex.get(r.key);
    return el('tr', {class: r.orphan || r.reserved ? 'orphan' : ''},
      el('td', {}, r.export),
      el('td', {}, r.label),
      el('td', {class: 'meta'}, r.source),
      el('td', {}, r.table),
      el('td', {}, r.address + (r.count > 1 ? '–' + (r.address + r.count - 1) : '')),
      el('td', {}, (FORMAT_TEXT[r.format] || r.format) + (r.scale && r.scale !== 1 && r.format !== 'bit' ? ' ×' + deNum(r.scale) : '')),
      el('td', {}, r.reserved ? '–' : showValue(r.value, info ? info.p.type : '', info ? info.p.value_list : null, r.unit)),
      el('td', {}, stateCell(r)),
      el('td', {}, r.reserved
        ? (info ? el('button', {type: 'button', onclick: ev => reAdd(r.key, ev.target)}, 'Wieder freigeben') : '')
        : el('button', {type: 'button', onclick: () => openEdit(r.key)}, 'Bearbeiten')));
  });
  $('exportRows').replaceChildren(...(rows.length ? rows : [el('tr', {}, el('td', {colspan: 9, class: 'empty'}, 'Noch keine Werte freigegeben. Unter „Geräte" oder „Systemvariablen" anhaken.'))]));
}

async function reAdd(key, btn) {
  const fake = {checked: true, disabled: false};
  btn.disabled = true;
  await toggle(key, true, fake);
  btn.disabled = false;
}

let editKey = null;

function openEdit(key) {
  const e = selected.get(key), info = keyIndex.get(key);
  if (!e) return;
  editKey = key;
  const type = e.type, ccuWrite = info ? !!info.p.writable : false;
  $('eTitle').textContent = e.label || key;
  $('eLabel').value = e.label || '';
  const w = $('eWritable');
  w.checked = !!e.writable;
  w.disabled = type === 'ACTION' || !ccuWrite;
  if (type === 'ACTION') w.checked = true;
  const f = $('eFormat');
  f.replaceChildren(...(FORMATS[type] || [e.format]).map(x => new Option(FORMAT_TEXT[x] || x, x)));
  f.value = e.format;
  $('eScale').value = e.scale || 1;
  const p = info ? info.p : {};
  const range = (p.min !== undefined || p.max !== undefined) ? ' · Bereich ' + deNum(p.min ?? '?') + ' … ' + deNum(p.max ?? '?') + (p.unit ? ' ' + p.unit : '') : '';
  $('eInfo').textContent = (TYPE_TEXT[type] || type) + range + (ccuWrite ? '' : ' · die CCU erlaubt hier kein Schreiben') + (p.value_list ? ' · Werte: ' + p.value_list.map((v, i) => i + '=' + v).join(', ') : '');
  scaleState();
  $('editDialog').showModal();
}

function scaleState() {
  const f = $('eFormat').value, s = $('eScale');
  s.disabled = f === 'bit' || f === 'float32';
  if (s.disabled) s.value = f === 'bit' ? 0 : 1;
}

async function applyEdit(remove) {
  const key = editKey;
  try {
    if (remove) {
      await api('mapping', {action: 'remove', key});
    } else {
      const format = $('eFormat').value;
      const scale = parseFloat(String($('eScale').value).replace(',', '.'));
      await api('mapping', {action: 'add', key, label: $('eLabel').value.trim(), format, scale: format === 'bit' ? 0 : (isFinite(scale) ? scale : 1), writable: $('eWritable').checked});
    }
    $('editDialog').close();
    await reloadMapping();
    notice(remove ? 'Freigabe entfernt.' : 'Übernommen.');
  } catch (e) {
    notice('Nicht übernommen: ' + e.message, true);
  }
}

// ---------------------------------------------------------------- client sheet

// third field: true = free text from the CCU or the user (CSV-neutralized)
const SHEET_COLS = [
  ['Nr.', r => r.export],
  ['Bezeichnung', r => r.label, true],
  ['Quelle (CCU)', r => r.source, true],
  ['Status', r => r.reserved ? 'reserviert (abgewählt)' : r.orphan ? 'fehlt in der CCU – Adresse reserviert' : 'aktiv', true],
  ['Tabelle', r => r.table],
  ['FC lesen', r => r.fc_read || '–'],
  ['FC schreiben', r => r.fc_write || '–'],
  ['Adresse (0-basiert)', r => r.address],
  ['Klassisch', r => r.classic],
  ['Anzahl', r => r.count],
  ['Format', r => r.format.toUpperCase()],
  ['Faktor', r => r.format === 'bit' ? '' : deNum(r.scale || 1)],
  ['Einheit', r => r.unit || '', true],
  ['MIN', r => r.min !== undefined && r.min !== null ? deNum(r.min) : ''],
  ['MAX', r => r.max !== undefined && r.max !== null ? deNum(r.max) : ''],
  ['Werteliste', r => (r.value_list || []).map((v, i) => i + '=' + v).join(', '), true],
  ['Zugriff', r => r.access],
  ['Gültig-Bit (DI)', r => r.valid_bit],
  ['Warn-Bit (DI)', r => r.warn_bit],
];

function sheetHeader() {
  const wo = (map && map.word_order) || 'ABCD';
  return [
    ['Server', 'CCU-ModbusServer ' + B.dataset.version],
    ['IP-Adresse', location.hostname],
    ['Port', PORT],
    ['Protokoll', 'Modbus TCP'],
    ['Unit-ID', 'beliebig (empfohlen 1)'],
    ['Bytefolge', 'Big-Endian (Modbus-Standard)'],
    ['Wortfolge 32 Bit', wo === 'CDAB' ? 'CDAB (Low-Word zuerst)' : 'ABCD (High-Word zuerst)'],
    ['Statusblock', 'Input Register 0: Bit 0 = CCU-Logik erreichbar; 1–2: Laufzeit in s (UINT32); 3: Anzahl Freigaben'],
    ['Gültig-/Warn-Bits', 'Discrete Input 10000+Nr. = Wert gültig; 20000+Nr. = Kommunikationswarnung der CCU'],
    ['Ungültige Werte', 'FLOAT32 = NaN; Ganzzahl und Bit = letzter gültiger Wert (0 vor dem ersten) – Gültig-Bit beachten'],
    ['Stand', new Date().toLocaleString('de-DE')],
  ];
}

// all rows, including deselected and vanished ones: their addresses stay
// reserved and must be visible to whoever configures the client
function sheetRows() {
  return sheet.slice().sort((a, b) => a.export - b.export);
}

function renderSheet() {
  const head = el('table', {class: 'sheethead'}, sheetHeader().map(([k, v]) => el('tr', {}, el('th', {}, k), el('td', {}, v))));
  const rows = sheetRows();
  const tbl = el('table', {class: 'sheettable'},
    el('thead', {}, el('tr', {}, SHEET_COLS.map(c => el('th', {}, c[0])))),
    el('tbody', {}, rows.map(r => el('tr', {class: r.orphan || r.reserved ? 'orphan' : ''}, SHEET_COLS.map(c => el('td', {}, c[1](r)))))));
  $('sheet').replaceChildren(
    el('h2', {class: 'printonly'}, 'CCU-ModbusServer – Client-Einstellungen'),
    head,
    rows.length ? el('div', {class: 'tablewrap sheetwrap'}, tbl) : el('div', {class: 'empty'}, 'Noch keine Werte freigegeben.'));
}

// Text that a spreadsheet would read as a formula (=, +, -, @, tab, CR)
// gets a leading apostrophe; numbers of our own columns stay numbers.
function csvField(v, text) {
  let s = String(v ?? '');
  if (text && /^[=+\-@\t\r]/.test(s)) s = "'" + s;
  return /[;"\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
}

function downloadCSV() {
  const lines = sheetHeader().map(([k, v]) => csvField(k, true) + ';' + csvField(v, true));
  lines.push('');
  lines.push(SHEET_COLS.map(c => csvField(c[0], true)).join(';'));
  for (const r of sheetRows()) lines.push(SHEET_COLS.map(c => csvField(c[1](r), c[2])).join(';'));
  const blob = new Blob(['﻿' + lines.join('\r\n') + '\r\n'], {type: 'text/csv;charset=utf-8'});
  const a = el('a', {href: URL.createObjectURL(blob), download: 'CCU-ModbusServer-' + location.hostname + '-' + new Date().toISOString().slice(0, 10) + '.csv'});
  document.body.append(a);
  a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
}

// ---------------------------------------------------------------- settings

async function saveAllow() {
  const list = $('allowList').value.split(/[\n,]+/).map(s => s.trim()).filter(Boolean);
  const out = $('allowResult');
  out.textContent = '';
  try {
    const c = await api('writeallow', {write_allow: list});
    $('allowList').value = (c.write_allow || []).join('\n');
    out.textContent = c.write_allow.length ? 'Gespeichert und aktiv.' : 'Gespeichert: niemand darf schreiben.';
    out.className = 'ok';
  } catch (e) {
    out.textContent = e.message;
    out.className = 'bad';
  }
}

async function saveWordOrder() {
  try {
    await api('mapping', {action: 'word_order', word_order: $('wordOrder').value});
    await reloadMapping();
    notice('Wortreihenfolge übernommen. Bitte den Modbus-Client entsprechend einstellen.');
  } catch (e) {
    notice('Nicht übernommen: ' + e.message, true);
    $('wordOrder').value = map.word_order || 'ABCD';
  }
}

async function loadWriteLog() {
  try {
    const log = await api('writelog') || [];
    $('writeLog').replaceChildren(...(log.length ? log.slice().reverse().map(l => el('tr', {},
      el('td', {}, new Date(l.time).toLocaleString('de-DE')), el('td', {}, l.client), el('td', {}, deNum(l.value)),
      el('td', {}, l.label), el('td', {class: /^ok|unverändert/.test(l.result) ? 'ok' : 'bad'}, l.result))) : [el('tr', {}, el('td', {colspan: 5, class: 'empty'}, 'Noch keine Schreibvorgänge.'))]));
  } catch (e) { /* shown elsewhere */ }
}

// ---------------------------------------------------------------- start

let tab = 'devices';

function showTab(name) {
  tab = name;
  document.querySelectorAll('.tabs button').forEach(b => {
    const on = b.dataset.tab === name;
    b.classList.toggle('active', on);
    b.setAttribute('aria-selected', on);
  });
  document.querySelectorAll('.tab').forEach(s => s.classList.toggle('hidden', s.id !== 'tab-' + name));
  if (name === 'settings') loadWriteLog();
}

// values and quality change all the time: refresh what is visible
async function tick() {
  if (document.hidden || busy || $('editDialog').open) return;
  try {
    const st = await api('status');
    showStatus(st);
    if (tab === 'exports' || tab === 'sheet') {
      const s = await api('sheet');
      sheet = s.rows || [];
      renderExports();
      if (tab === 'sheet') renderSheet();
    } else if (tab === 'settings') {
      loadWriteLog();
    }
  } catch (e) {
    $('ccuState').textContent = e.message;
    $('ccuState').className = 'bad';
  }
}

function debounce(f, ms) { let t; return () => { clearTimeout(t); t = setTimeout(f, ms); }; }

document.querySelectorAll('.tabs button').forEach(b => b.addEventListener('click', () => showTab(b.dataset.tab)));
['fRoom', 'fFunc', 'fType', 'fSel'].forEach(id => $(id).addEventListener('change', renderDevices));
$('fName').addEventListener('input', debounce(renderDevices, 200));
$('svName').addEventListener('input', debounce(renderSysvars, 200));
$('svInternal').addEventListener('change', renderSysvars);
$('eFormat').addEventListener('change', scaleState);
$('eApply').addEventListener('click', () => applyEdit(false));
$('eRemove').addEventListener('click', () => applyEdit(true));
$('saveAllow').addEventListener('click', saveAllow);
$('wordOrder').addEventListener('change', saveWordOrder);
$('csvBtn').addEventListener('click', downloadCSV);
$('printBtn').addEventListener('click', () => { renderSheet(); window.print(); });

loadAll().catch(e => {
  notice('Daten nicht abrufbar: ' + e.message, true);
  $('deviceList').replaceChildren(el('div', {class: 'empty'}, 'Geräteliste nicht verfügbar. Läuft der Server und ist die CCU-Logik gestartet?'));
});
setInterval(tick, 5000);

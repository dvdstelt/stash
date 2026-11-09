// Shared helpers for the owner app and the public upload/share pages.
// Loaded as a classic script (not an ES module) so the pages also work when opened straight from disk.

(() => {
class Safe {
  constructor(s) { this.s = s; }
  toString() { return this.s; }
}

const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ESC[c]);
const raw = (s) => new Safe(s);
/** A boolean as an attribute value. */
const b = (v) => (v ? 'true' : 'false');

function part(v) {
  if (v == null || v === false || v === true) return '';
  if (Array.isArray(v)) return v.map(part).join('');
  if (v instanceof Safe) return v.s;
  return esc(v);
}

/** Template tag that escapes every interpolated value unless it is already Safe. */
function html(strings, ...values) {
  let out = strings[0];
  for (let i = 0; i < values.length; i++) out += part(values[i]) + strings[i + 1];
  return new Safe(out);
}

/* ---------- Icons ---------- */

const PATHS = {
  folder: '<path d="M3 7.5A2.5 2.5 0 0 1 5.5 5H9l2 2h7.5A2.5 2.5 0 0 1 21 9.5v8a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  doc: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h4"/>',
  sheet: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M8.5 12h7v6h-7zM8.5 15h7M12 12v6"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><circle cx="9" cy="10" r="1.8"/><path d="m21 15.5-4.6-4.6a1.5 1.5 0 0 0-2.1 0L6 19.5"/>',
  video: '<rect x="3" y="5" width="13" height="14" rx="2.5"/><path d="m16 10 5-3v10l-5-3"/>',
  archive: '<rect x="3" y="4" width="18" height="5" rx="1.5"/><path d="M5 9v9.5A1.5 1.5 0 0 0 6.5 20h11a1.5 1.5 0 0 0 1.5-1.5V9M10 13h4"/>',
  database: '<ellipse cx="12" cy="5.5" rx="7.5" ry="2.5"/><path d="M4.5 5.5v13c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5v-13M4.5 12c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5"/>',
  upload: '<path d="M12 15V4M7.5 8.5 12 4l4.5 4.5M4 15v3.5A2.5 2.5 0 0 0 6.5 21h11a2.5 2.5 0 0 0 2.5-2.5V15"/>',
  download: '<path d="M12 4v11M7.5 10.5 12 15l4.5-4.5M4 15v3.5A2.5 2.5 0 0 0 6.5 21h11a2.5 2.5 0 0 0 2.5-2.5V15"/>',
  inbox: '<path d="M21.5 12.5h-5.5l-2 3h-4l-2-3H2.5"/><path d="M5.6 5.2 2.5 12.5v5A2.5 2.5 0 0 0 5 20h14a2.5 2.5 0 0 0 2.5-2.5v-5l-3.1-7.3A2 2 0 0 0 16.6 4H7.4a2 2 0 0 0-1.8 1.2z"/>',
  link: '<path d="M10 13.5a4 4 0 0 0 6 .4l3-3a4 4 0 0 0-5.7-5.7l-1.7 1.7"/><path d="M14 10.5a4 4 0 0 0-6-.4l-3 3a4 4 0 0 0 5.7 5.7l1.7-1.7"/>',
  copy: '<rect x="8.5" y="8.5" width="12" height="12" rx="2.5"/><path d="M15.5 8.5V6a2.5 2.5 0 0 0-2.5-2.5H6A2.5 2.5 0 0 0 3.5 6v7A2.5 2.5 0 0 0 6 15.5h2.5"/>',
  check: '<path d="M20 6.5 9.5 17 4 11.5"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  trash: '<path d="M4 6.5h16M9.5 6.5V4.5h5v2M6.5 6.5l.8 12.6A2 2 0 0 0 9.3 21h5.4a2 2 0 0 0 2-1.9l.8-12.6"/>',
  clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>',
  lock: '<rect x="4.5" y="10.5" width="15" height="10" rx="2.5"/><path d="M8 10.5V7.5a4 4 0 0 1 8 0v3"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="m20 20-4.2-4.2"/>',
  grid: '<rect x="4" y="4" width="6.5" height="6.5" rx="1.5"/><rect x="13.5" y="4" width="6.5" height="6.5" rx="1.5"/><rect x="4" y="13.5" width="6.5" height="6.5" rx="1.5"/><rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.5"/>',
  list: '<path d="M9 6.5h11M9 12h11M9 17.5h11M4.5 6.5h.01M4.5 12h.01M4.5 17.5h.01"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  'chevron-right': '<path d="m9.5 6 6 6-6 6"/>',
  'chevron-left': '<path d="m14.5 6-6 6 6 6"/>',
  'chevron-down': '<path d="m6 9.5 6 6 6-6"/>',
  'chevron-up': '<path d="m6 14.5 6-6 6 6"/>',
  'arrow-left': '<path d="M19 12H5M11 18l-6-6 6-6"/>',
  external: '<path d="M14 4h6v6M20 4l-9 9M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4"/>',
  drive: '<path d="M4.5 13.5 7 5.5A2 2 0 0 1 8.9 4h6.2A2 2 0 0 1 17 5.5l2.5 8"/><rect x="3" y="13.5" width="18" height="6.5" rx="2"/><path d="M7 16.75h.01M10.5 16.75h.01"/>',
  menu: '<path d="M4 7h16M4 12h16M4 17h16"/>',
  alert: '<circle cx="12" cy="12" r="8.5"/><path d="M12 8v4.5M12 16h.01"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="m7.5 9.5 3 2.5-3 2.5M12.5 15h4"/>',
  ban: '<circle cx="12" cy="12" r="8.5"/><path d="m6 6 12 12"/>',
  logout: '<path d="M9.5 20.5H6A2.5 2.5 0 0 1 3.5 18V6A2.5 2.5 0 0 1 6 3.5h3.5M15.5 16.5 20 12l-4.5-4.5M20 12H9"/>',
  briefcase: '<rect x="3" y="7" width="18" height="13" rx="2.5"/><path d="M8.5 7V5.5A1.5 1.5 0 0 1 10 4h4a1.5 1.5 0 0 1 1.5 1.5V7M3 12.5h18M10.5 12.5v1.5h3v-1.5"/>',
  contrast: '<circle cx="12" cy="12" r="8.5"/><path d="M12 3.5a8.5 8.5 0 0 1 0 17z" style="fill:currentColor"/>',
};

const icon = (name, cls = '') =>
  raw(`<svg class="icon ${cls}" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${PATHS[name] ?? PATHS.file}</svg>`);

/** Replace <span data-icon="name"> placeholders in static HTML. */
function hydrateIcons(root = document) {
  root.querySelectorAll('[data-icon]').forEach((el) => { el.outerHTML = icon(el.dataset.icon, el.className).s; });
}

/* ---------- Formatting ---------- */

function bytes(n) {
  if (!n) return '0 B';
  if (n < 1000) return `${Math.round(n)} B`;
  const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let i = -1;
  do { n /= 1000; i++; } while (n >= 999.95 && i < units.length - 1);
  return `${n >= 100 ? Math.round(n) : n.toFixed(1).replace(/\.0$/, '')} ${units[i]}`;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
const UNITS = [['year', 365 * 864e5], ['month', 30 * 864e5], ['week', 7 * 864e5], ['day', 864e5], ['hour', 36e5], ['minute', 6e4]];
function rel(ts) {
  const diff = ts - Date.now();
  for (const [unit, ms] of UNITS) if (Math.abs(diff) >= ms) return rtf.format(Math.round(diff / ms), unit);
  return 'just now';
}

const dtf = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
const date = (ts) => dtf.format(ts);

const slug = (s) => (s || '').toLowerCase().normalize('NFKD').replace(/[^\w]+/g, '-').replace(/^-+|-+$/g, '') || 'files';

/* ---------- File kinds, thumbnails and previews ---------- */

// SVG is deliberately not an image kind: served inline it can run scripts.
const KINDS = {
  image: ['jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'heic', 'bmp'],
  pdf: ['pdf'],
  doc: ['doc', 'docx', 'odt', 'rtf', 'pages'],
  text: ['txt', 'md', 'log', 'csv', 'json', 'xml', 'yaml', 'yml'],
  sheet: ['xls', 'xlsx', 'ods', 'numbers'],
  archive: ['zip', 'tar', 'gz', 'tgz', 'zst', '7z', 'rar', 'bz2', 'xz'],
  db: ['dump', 'sql', 'bak', 'bacpac', 'db', 'sqlite', 'mdf'],
  video: ['mp4', 'mov', 'mkv', 'webm', 'avi'],
};
const KIND_ICON = { folder: 'folder', image: 'image', pdf: 'doc', doc: 'doc', text: 'doc', sheet: 'sheet', archive: 'archive', db: 'database', video: 'video', file: 'file' };

function kindOf(name) {
  const ext = name.includes('.') ? name.split('.').pop().toLowerCase() : '';
  for (const [kind, exts] of Object.entries(KINDS)) if (exts.includes(ext)) return kind;
  return 'file';
}

const imgSrc = (it) => it.src;

function thumb(it) {
  const kind = it.type === 'folder' ? 'folder' : kindOf(it.name);
  if (kind === 'image' && it.src) return html`<img src="${imgSrc(it)}" alt="" loading="lazy" decoding="async">`;
  return html`<span class="kind kind-${kind}">${icon(KIND_ICON[kind])}</span>`;
}

function previewMedia(it) {
  const kind = kindOf(it.name);
  if (kind === 'image' && it.src) return html`<img class="pv-img" src="${imgSrc(it)}" alt="${it.name}">`;
  if (kind === 'pdf' && it.src) return html`<iframe class="pv-frame" src="${it.src}" title="${it.name}"></iframe>`;
  if (kind === 'video' && it.src) return html`<video class="pv-img" src="${it.src}" controls></video>`;
  if (kind === 'text') return html`<pre class="pv-text" data-text-preview>${it.text ?? 'Loading…'}</pre>`;
  return html`<div class="pv-none"><span class="kind kind-${kind}">${icon(KIND_ICON[kind])}</span><p>No preview for this file.<br>Download it to open it.</p></div>`;
}

/* ---------- Command-line snippets ---------- */

const TAB_LABELS = { unix: 'macOS / Linux', windows: 'Windows', powershell: 'PowerShell 7', wget: 'wget' };

// curl ships with macOS, most Linux systems, and Windows 10 and later (as curl.exe, which works in
// both Command Prompt and PowerShell). It streams files of any size. Each tab is a single command,
// because comment lines break when pasted into zsh (the macOS default) or Command Prompt.
function uploadSnippets(url, { password } = {}) {
  const file = 'export.sql';
  const h = password ? ' -H "Link-Password: YOUR-PASSWORD"' : '';
  return {
    unix: `curl${h} -T ./${file} ${url}/`,
    windows: `curl.exe${h} -T .\\${file} ${url}/`,
    powershell: ['Invoke-RestMethod -Method Put -InFile .\\' + file + ' `', ...(password ? ["  -Headers @{ 'Link-Password' = 'YOUR-PASSWORD' } `"] : []), `  -Uri ${url}/${file}`].join('\n'),
    wget: [`wget --method=PUT --body-file=./${file} -O - \\`, ...(password ? ['  --header="Link-Password: YOUR-PASSWORD" \\'] : []), `  ${url}/${file}`].join('\n'),
  };
}

function shareSnippets(url, { name, password } = {}) {
  const out = `${slug(name)}.zip`;
  const zip = `${url}/all.zip`;
  const h = password ? ' -H "Link-Password: YOUR-PASSWORD"' : '';
  return {
    unix: `curl -L${h} -o ${out} ${zip}`,
    windows: `curl.exe -L${h} -o ${out} ${zip}`,
    powershell: ['Invoke-WebRequest -OutFile ' + out + ' `', ...(password ? ["  -Headers @{ 'Link-Password' = 'YOUR-PASSWORD' } `"] : []), `  -Uri ${zip}`].join('\n'),
    wget: password ? `wget --header="Link-Password: YOUR-PASSWORD" -O ${out} ${zip}` : `wget -O ${out} ${zip}`,
  };
}

// Start on the tab that matches the visitor's computer.
const defaultTab = /Windows/.test(navigator.userAgent) ? 'windows' : 'unix';

function snippetTabs(snippets, active = defaultTab) {
  const keys = Object.keys(snippets);
  return html`<div class="snippets">
    <div class="tabs" role="tablist">${keys.map((k) => html`<button type="button" role="tab" class="tab" aria-selected="${b(k === active)}" data-action="snippet-tab" data-tab="${k}">${TAB_LABELS[k]}</button>`)}</div>
    ${keys.map((k) => html`<div class="code-wrap" role="tabpanel" data-tab="${k}" ${k === active ? '' : raw('hidden')}>
      <pre class="code"><code>${snippets[k]}</code></pre>
      <button type="button" class="icon-btn copy" data-action="copy" data-text="${snippets[k]}" data-msg="Command copied" aria-label="Copy command" title="Copy">${icon('copy')}</button>
    </div>`)}
  </div>`;
}

/* ---------- Public page building blocks ---------- */

const brandHeader = () => html`<header class="public-brand"><span class="brand-mark">${icon('briefcase')}</span><span><strong>Stash</strong> <span class="muted">· ${location.host}</span></span></header>`;

const stateCard = (iconName, title, text) =>
  html`<section class="card state"><span class="state-icon">${icon(iconName)}</span><h1>${title}</h1><p>${text}</p></section>`;

const passwordGate = (title, text) => html`<section class="card state">
  <span class="state-icon">${icon('lock')}</span>
  <h1>${title}</h1>
  <p>${text}</p>
  <form class="unlock" data-submit="unlock">
    <input class="input" type="password" name="pw" required autocomplete="off" placeholder="Password" aria-label="Password" autofocus>
    <button class="btn btn-primary" type="submit">Continue</button>
  </form>
  <p class="form-error" role="alert" data-error hidden></p>
</section>`;

/* ---------- Upload rows (one per file, each with its own progress bar) ---------- */

// An upload is { id, name, size, loaded, status: 'uploading' | 'done' | 'error', error?, doneText? }.

/** Prototype only: bytes per second, so small files take about a second and huge ones under ten. */
const simulatedRate = (size) => Math.max(1, size / (1.2 + 0.9 * Math.log10(Math.max(size, 1e3) / 1e3)));

const uploadPercent = (u) => (u.size ? Math.floor((u.loaded / u.size) * 100) : 100);

function uploadStateText(u) {
  if (u.status === 'done') return u.doneText ?? `${bytes(u.size)} · Uploaded`;
  if (u.status === 'error') return bytes(u.size);
  return `${bytes(u.loaded)} of ${bytes(u.size)} · ${uploadPercent(u)}%`;
}

function uploadRow(u) {
  const kind = kindOf(u.name);
  return html`<div class="upl ${u.status}" data-upl="${u.id}">
    <span class="kind kind-${kind}">${icon(KIND_ICON[kind])}</span>
    <div class="min0">
      <div class="upl-top"><span class="ellipsis strong" title="${u.name}">${u.name}</span><span class="upl-state">${uploadStateText(u)}</span></div>
      ${u.status === 'error'
        ? html`<div class="upl-error">${u.error}</div>`
        : html`<div class="meter thin${u.status === 'done' ? ' done' : ''}"><span style="width:${uploadPercent(u)}%"></span></div>`}
    </div>
    ${u.status === 'uploading'
      ? html`<button type="button" class="icon-btn btn-ghost" data-action="remove-upload" data-id="${u.id}" aria-label="Cancel uploading ${u.name}" title="Cancel">${icon('x')}</button>`
      : u.status === 'error'
        ? html`<button type="button" class="icon-btn btn-ghost" data-action="remove-upload" data-id="${u.id}" aria-label="Dismiss" title="Dismiss">${icon('x')}</button>`
        : html`<span class="done-mark">${icon('check')}</span>`}
  </div>`;
}

/** Update one row's bar and text in place, so a click on its cancel button isn't lost to a re-render. */
function updateUploadRow(root, u) {
  const el = root.querySelector(`[data-upl="${u.id}"]`);
  if (!el) return;
  el.querySelector('.meter span').style.width = `${uploadPercent(u)}%`;
  el.querySelector('.upl-state').textContent = uploadStateText(u);
}

/* ---------- Drop area ---------- */

/**
 * An area to drag files onto, with its own button to pick them instead.
 * Pages handle `change` on [data-file-input] and drag events on [data-dropzone].
 */
function dropZone({ title, hint, button = 'Choose files', compact = false, disabled = false }) {
  return html`<div class="dropzone${compact ? ' compact' : ''}${disabled ? ' disabled' : ''}" data-dropzone ${disabled ? '' : raw('data-action="choose-files"')}>
    <input type="file" multiple hidden data-file-input ${disabled ? raw('disabled') : ''}>
    <span class="dz-icon">${icon('upload')}</span>
    <span class="dz-text"><strong>${title}</strong><span class="hint">${hint}</span></span>
    ${disabled ? '' : html`<button type="button" class="btn btn-primary">${icon('upload')} ${button}</button>`}
  </div>`;
}

/* ---------- Feedback, events, theme ---------- */

function toast(message, iconName = 'check') {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    el.className = 'toast';
    el.setAttribute('role', 'status');
    el.setAttribute('popover', 'manual'); // top layer, so it shows above open dialogs
    document.body.append(el);
  }
  el.innerHTML = html`${icon(iconName)}<span>${message}</span>`.s;
  if (el.showPopover) {
    if (el.matches(':popover-open')) el.hidePopover();
    el.showPopover();
  }
  el.classList.add('show');
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => {
    el.classList.remove('show');
    if (el.hidePopover && el.matches(':popover-open')) el.hidePopover();
  }, 2200);
}

async function copy(text, message = 'Copied') {
  try {
    await navigator.clipboard.writeText(text);
    toast(message);
  } catch {
    toast('Couldn’t copy. Select the text and copy it yourself.', 'alert');
  }
}

function toggleTheme() {
  const root = document.documentElement;
  const current = root.dataset.theme ?? (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
  root.dataset.theme = current === 'dark' ? 'light' : 'dark';
  try { localStorage.setItem('theme', root.dataset.theme); } catch { /* storage unavailable */ }
}

const common = {
  copy: (el) => copy(el.dataset.text, el.dataset.msg),
  'snippet-tab': (el) => {
    const root = el.closest('.snippets');
    root.querySelectorAll('[role=tab]').forEach((t) => t.setAttribute('aria-selected', b(t === el)));
    root.querySelectorAll('[role=tabpanel]').forEach((p) => { p.hidden = p.dataset.tab !== el.dataset.tab; });
  },
  'select-text': (el) => el.select(),
  // Any click in a drop area opens the file picker. The hidden input's own click bubbles back here, so skip it.
  'choose-files': (el, e) => {
    if (!e.target.matches('input')) el.querySelector('[data-file-input]')?.click();
  },
  theme: () => toggleTheme(),
};

/** One click/submit listener per page; elements name their handler with data-action / data-submit. */
function delegate(actions = {}, submits = {}) {
  document.addEventListener('click', (e) => {
    const el = e.target.closest('[data-action]');
    const fn = el && (actions[el.dataset.action] ?? common[el.dataset.action]);
    if (fn) fn(el, e);
  });
  document.addEventListener('keydown', (e) => {
    if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('[role=button][data-action]')) {
      e.preventDefault();
      e.target.click();
    }
  });
  document.addEventListener('submit', (e) => {
    const fn = submits[e.target.dataset.submit];
    if (fn) {
      e.preventDefault();
      fn(e.target);
    }
  });
}

window.StashUI = {
  Safe, esc, raw, b, html, icon, hydrateIcons, bytes, rel, date, slug, KIND_ICON, kindOf, imgSrc, thumb, previewMedia,
  uploadSnippets, shareSnippets, snippetTabs, brandHeader, stateCard, passwordGate, toast, copy, toggleTheme, delegate,
  simulatedRate, uploadRow, updateUploadRow, dropZone,
};
})();

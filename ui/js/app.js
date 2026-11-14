// Owner app: your files, the upload links you hand out, and the share links you send.

const {
  html, raw, b, icon, bytes, rel, date, kindOf, thumb, previewMedia,
  snippetTabs, uploadSnippets, shareSnippets, toast, copy, delegate, hydrateIcons,
  uploadRow, updateUploadRow, dropZone, slug, qrSvg, downloadQr,
} = window.StashUI;
const { store, uploadUrl, shareUrl, MB, GB, TB } = window.StashData;
const { request, uploadFile } = window.StashAPI;
const $ = (sel) => document.querySelector(sel);
const viewEl = $('#view');
const modal = $('#modal');
const searchInput = $('#search');

const state = {
  layout: load('layout') === 'list' ? 'list' : 'grid',
  selected: new Set(),
  linkFilter: 'all',
  query: '',
  folder: 'root',
};
let pendingConfirm = null;

function load(key) { try { return localStorage.getItem(key); } catch { return null; } }
function save(key, value) { try { localStorage.setItem(key, value); } catch { /* storage unavailable */ } }

/* ---------- Routing ---------- */

function route() {
  const [section = 'files', id = null] = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  return { section, id };
}

function render() {
  closeMenu();
  const { section, id } = route();
  let content;
  if (state.query) content = searchView();
  else if (section === 'upload-links') content = id ? requestDetail(id) : requestList();
  else if (section === 'share-links') content = id ? shareDetail(id) : shareList();
  else if (section === 'trash') content = trashView();
  else content = filesView(id ?? 'root');
  viewEl.innerHTML = content.s;
  renderSidebar(state.query ? null : section);
}

function navigate(hash) {
  clearSearch();
  if (location.hash === hash) render();
  else location.hash = hash;
}

function clearSearch() {
  state.query = '';
  searchInput.value = '';
}

function renderSidebar(section) {
  document.querySelectorAll('.nav a').forEach((a) => {
    if (a.dataset.section === section) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  });
  const open = store.requests().filter((r) => store.status(r) === 'active');
  $('#count-upload').textContent = open.length || '';
  $('#count-share').textContent = store.shares().filter((s) => store.shareStatus(s) === 'active').length || '';
  const trash = store.trash();
  $('#count-trash').textContent = trash.length || '';
  const trashBytes = trash.reduce((sum, it) => sum + store.contents(it.id).bytes, 0);

  const used = store.used();
  const capacity = store.capacity() || 1;
  const promised = open.reduce((sum, r) => sum + Math.max(0, r.quota - store.usage(r).used), 0);
  const pct = (n) => `${Math.min(100, (n / capacity) * 100).toFixed(2)}%`;
  $('#storage').innerHTML = html`
    <div class="storage-head"><span>Storage</span><span class="muted">${bytes(used)} used</span></div>
    <div class="meter" title="Used, plus what open upload links could still add"><span style="width:${pct(used)}"></span><span class="reserved" style="width:${pct(promised)}"></span></div>
    <p class="hint">${bytes(store.free())} free on the disk. Open upload links can add up to ${bytes(promised)}.${trashBytes ? ` The trash holds ${bytes(trashBytes)}.` : ''}</p>`.s;
}

/** Runs a change on the server and shows the result. Returns the result, or false if it failed. */
async function attempt(work, success) {
  try {
    const result = await work();
    if (success) toast(success);
    render();
    return result ?? true;
  } catch (err) {
    if (err.status === 401) location.href = '/login';
    else toast(err.message, 'alert');
    return false;
  }
}

function failedToLoad(err) {
  if (err.status === 401) {
    location.href = '/login';
    return;
  }
  viewEl.innerHTML = html`<div class="card empty">${icon('alert')}<p><strong>Couldn’t load your files</strong><br>${err.message}</p></div>`.s;
}

/* ---------- Shared bits ---------- */

const STATUS = { active: ['ok', 'Active'], full: ['warn', 'Full'], expired: ['muted', 'Expired'], revoked: ['danger', 'Revoked'] };
function badge(status, fullLabel = 'Full') {
  const [cls, label] = STATUS[status];
  return html`<span class="badge ${cls}">${status === 'full' ? fullLabel : label}</span>`;
}

const expiresText = (ts) => (!ts ? 'Never' : ts <= Date.now() ? `Ended ${rel(ts)}` : rel(ts));
const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;
const pathText = (folderId) => store.path(folderId).map((p) => p.name).join(' / ');
const total = (files) => files.reduce((sum, f) => sum + f.size, 0);

function meter(used, quota) {
  const pct = quota ? Math.min(100, (used / quota) * 100) : 0;
  const cls = pct >= 100 ? 'full' : pct >= 80 ? 'warn' : '';
  return html`<div class="meter ${cls}" role="progressbar" aria-label="Space used" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(pct)}"><span style="width:${pct.toFixed(1)}%"></span></div>`;
}

const modalHead = (title) => html`<div class="modal-head"><h2>${title}</h2><button type="button" class="icon-btn btn-ghost" data-action="close-modal" aria-label="Close">${icon('x')}</button></div>`;

function showModal(content, cls = '') {
  modal.className = `modal ${cls}`;
  modal.innerHTML = content.s;
  if (!modal.open) modal.showModal();
  modal.querySelector('[autofocus]')?.focus();
}

function confirmModal({ title, body, confirm, onConfirm }) {
  pendingConfirm = onConfirm;
  showModal(html`${modalHead(title)}
    <div class="modal-body"><p>${body}</p></div>
    <div class="modal-foot"><button type="button" class="btn" data-action="close-modal" autofocus>Cancel</button><button type="button" class="btn btn-danger-solid" data-action="confirm">${confirm}</button></div>`, 'narrow');
}

function linkPanel(url, name, snippets, previewHref) {
  return html`<div class="link-panel">
    <div class="linkbox">
      <div class="link-field">
        <input class="input mono" readonly value="${url}" aria-label="Link, click to copy" title="Click to copy" data-action="copy-link">
        <button type="button" class="icon-btn btn-ghost" data-action="copy-link" aria-label="Copy link" title="Copy link">${icon('copy')}</button>
      </div>
      <div class="linkbox-actions">
        <button type="button" class="btn btn-ghost" data-action="toggle-qr" aria-expanded="false" title="Show the link as a QR code, to open it on a phone or print it">${icon('qr')} QR code</button>
        <a class="btn btn-ghost" href="${previewHref}" target="_blank" rel="noopener" title="Open the page the recipient sees">${icon('external')} Preview</a>
      </div>
    </div>
    <div class="qr-panel" data-qr hidden>
      ${qrSvg(url)}
      <div class="qr-text">
        <p><strong>Point a phone’s camera at the code</strong> to open the link on the phone.</p>
        <p class="hint">To put the link on paper, such as a poster or a sign, download the code and print it.</p>
        <button type="button" class="btn" data-action="download-qr" data-url="${url}" data-name="${name}">${icon('download')} Download image</button>
      </div>
    </div>
    ${snippetTabs(snippets)}
  </div>`;
}

function notFound(text, href, label) {
  return html`<div class="card empty">${icon('alert')}<p><strong>Not found</strong><br>${text}</p><a class="btn" href="${href}">Back to ${label}</a></div>`;
}

function invalid(input, message) {
  input.setCustomValidity(message);
  input.reportValidity();
  input.addEventListener('input', () => input.setCustomValidity(''), { once: true });
}

/* ---------- Files ---------- */

function filesView(folderId) {
  const folder = store.item(folderId);
  if (!folder || folder.type !== 'folder') return notFound('This folder doesn’t exist.', '#/files', 'My files');
  if (store.inTrash(folder.id)) return notFound('This folder is in the trash.', '#/trash', 'Trash');
  state.folder = folder.id;
  const crumbs = store.path(folder.id);
  const kids = store.children(folder.id);
  const req = store.requestForFolder(folder.id);
  return html`
    <div class="page-head">
      <div>
        ${crumbs.length > 1 ? html`<nav class="crumbs" aria-label="Folder path">${crumbs.slice(0, -1).map((c) => html`<a href="#/files/${c.id}">${c.name}</a>${icon('chevron-right')}`)}</nav>` : ''}
        <h1 class="ellipsis">${folder.name}</h1>
        ${req ? html`<p class="sub">${icon('inbox', 'sm')} Files sent through <a href="#/upload-links/${req.id}">this upload link</a> land here.</p>` : ''}
      </div>
      <div class="actions">
        <button type="button" class="btn" data-action="new-folder">${icon('plus')} New folder</button>
        ${layoutToggle()}
      </div>
    </div>
    ${kids.length
      ? html`${dropZone({ compact: true, title: `Drag files here to upload them to ${folder.name}`, hint: 'Or drop them anywhere on this page. As many files as you like.', button: 'Upload files' })}
             ${selectionBar()}
             ${itemsView(kids)}`
      : dropZone({ title: 'This folder is empty', hint: `Drag files here to upload them to ${folder.name}.`, button: 'Upload files' })}`;
}

const layoutToggle = () => html`<div class="seg" role="group" aria-label="Layout">
  <button type="button" class="icon-btn" data-action="layout" data-layout="grid" aria-pressed="${b(state.layout === 'grid')}" aria-label="Grid view" title="Grid view">${icon('grid')}</button>
  <button type="button" class="icon-btn" data-action="layout" data-layout="list" aria-pressed="${b(state.layout === 'list')}" aria-label="List view" title="List view">${icon('list')}</button>
</div>`;

function itemsView(items) {
  if (state.layout === 'list') return listView(items);
  return html`<div class="grid${state.selected.size ? ' has-sel' : ''}">${items.map((it) => tile(it))}</div>`;
}

function tile(it, selectable = true) {
  const sel = state.selected.has(it.id);
  const isFile = it.type === 'file';
  return html`<div class="tile${sel ? ' selected' : ''}" role="button" tabindex="0" data-action="open" data-id="${it.id}" aria-label="${isFile ? 'Preview' : 'Open'} ${it.name}">
    ${isFile && selectable ? html`<input type="checkbox" class="check" data-action="select" data-id="${it.id}" ${sel ? raw('checked') : ''} aria-label="Select ${it.name}">` : ''}
    ${store.requestForFolder(it.id) ? html`<span class="tile-flag" title="Upload link folder">${icon('inbox', 'xs')}</span>` : ''}
    <div class="thumb">${thumb(it)}</div>
    <div class="tile-body"><div class="min0"><div class="tile-name" title="${it.name}">${it.name}</div><div class="tile-meta">${itemMeta(it)}</div></div>${moreButton(it)}</div>
  </div>`;
}

const moreButton = (it) => html`<button type="button" class="icon-btn btn-ghost more" data-action="item-menu" data-id="${it.id}" aria-haspopup="menu" aria-expanded="false" aria-label="Actions for ${it.name}" title="More actions">${icon('more')}</button>`;

function itemMeta(it) {
  if (it.type === 'file') return `${bytes(it.size)} · ${rel(it.modified)}`;
  return `${plural(store.children(it.id).length, 'item')} · ${bytes(store.size(it.id))}`;
}

function listView(items, { selectable = true, showPath = false } = {}) {
  return html`<div class="table rows-files${selectable ? '' : ' no-select'}">
    <div class="row head">${selectable ? html`<span></span>` : ''}<span>Name</span><span>Size</span><span class="col-date">Modified</span><span></span></div>
    ${items.map((it) => {
      const sel = state.selected.has(it.id);
      return html`<div class="row link${sel ? ' selected' : ''}" role="button" tabindex="0" data-action="open" data-id="${it.id}">
        ${selectable ? html`<span>${it.type === 'file' ? html`<input type="checkbox" data-action="select" data-id="${it.id}" ${sel ? raw('checked') : ''} aria-label="Select ${it.name}">` : ''}</span>` : ''}
        <span class="name-cell"><span class="mini">${thumb(it)}</span><span class="min0 stack"><span class="ellipsis strong">${it.name}</span>${showPath ? html`<span class="ellipsis muted small">${pathText(it.parent)}</span>` : ''}</span></span>
        <span class="muted nowrap">${it.type === 'file' ? bytes(it.size) : bytes(store.size(it.id))}</span>
        <span class="muted nowrap col-date">${rel(it.modified)}</span>
        <span>${moreButton(it)}</span>
      </div>`;
    })}
  </div>`;
}

function selectionBar() {
  const n = state.selected.size;
  if (!n) return '';
  const size = total([...state.selected].map((id) => store.item(id)).filter(Boolean));
  return html`<div class="selbar">
    <strong>${n} selected</strong><span class="muted">${bytes(size)}</span><span class="spacer"></span>
    <button type="button" class="btn btn-sm" data-action="share-selected">${icon('link')} Create share link</button>
    <button type="button" class="btn btn-sm btn-ghost btn-danger" data-action="delete-selected">${icon('trash')} Delete</button>
    <button type="button" class="icon-btn btn-ghost" data-action="clear-selection" aria-label="Clear selection" title="Clear selection">${icon('x')}</button>
  </div>`;
}

function searchView() {
  const results = store.search(state.query);
  return html`
    <div class="page-head">
      <div><h1>Search</h1><p>${plural(results.length, 'result')} for “${state.query}”</p></div>
      <button type="button" class="btn" data-action="clear-search">${icon('x')} Clear search</button>
    </div>
    ${selectionBar()}
    ${results.length ? listView(results, { showPath: true }) : html`<div class="card empty">${icon('search')}<p>No files or folders match.</p></div>`}`;
}

function openPreview(id) {
  const it = store.item(id);
  const req = store.requestForFolder(it.parent);
  showModal(html`<div class="preview">
    <div class="pv-stage">${previewMedia(it)}</div>
    <aside class="pv-side">
      <div class="pv-top"><h2 class="pv-title">${it.name}</h2><button type="button" class="icon-btn btn-ghost" data-action="close-modal" aria-label="Close" autofocus>${icon('x')}</button></div>
      <dl class="facts-list">
        <div><dt>Size</dt><dd>${bytes(it.size)}</dd></div>
        <div><dt>${req ? 'Received' : 'Modified'}</dt><dd>${date(it.modified)} · ${rel(it.modified)}</dd></div>
        <div><dt>Location</dt><dd><a href="#/files/${it.parent}" data-action="close-modal">${pathText(it.parent)}</a></dd></div>
        ${req ? html`<div><dt>Sent through</dt><dd><a href="#/upload-links/${req.id}" data-action="close-modal">${req.name}</a></dd></div>` : ''}
      </dl>
      <div class="pv-actions">
        <a class="btn btn-primary" href="${store.contentUrl(it.id)}?download" download>${icon('download')} Download</a>
        <button type="button" class="btn" data-action="share-files" data-ids="${it.id}">${icon('link')} Create share link</button>
        <button type="button" class="btn" data-action="rename" data-id="${it.id}" data-reopen="true">${icon('pencil')} Rename</button>
        <button type="button" class="btn btn-ghost btn-danger" data-action="delete" data-ids="${it.id}">${icon('trash')} Delete</button>
      </div>
    </aside>
  </div>`, 'preview-modal');
  if (kindOf(it.name) === 'text' && it.text === undefined) loadText(it);
}

const TEXT_PREVIEW = 256 * 1024;

async function loadText(it) {
  try {
    const res = await fetch(store.contentUrl(it.id), { headers: { Range: `bytes=0-${TEXT_PREVIEW - 1}` } });
    if (!res.ok) throw new Error(res.statusText);
    it.text = await res.text();
    if (it.size > TEXT_PREVIEW) it.text += '\n\n… The preview shows the first 256 KB. Download the file to see all of it.';
  } catch {
    it.text = 'Couldn’t load a preview of this file.';
  }
  const el = modal.querySelector('[data-text-preview]');
  if (el) el.textContent = it.text;
}

function folderForm() {
  showModal(html`<form data-submit="save-folder">
    ${modalHead('New folder')}
    <div class="modal-body">
      <div class="field"><label for="d-name">Name</label><input id="d-name" name="name" class="input" required maxlength="255" autofocus></div>
      <p class="hint">It’s created in ${store.item(state.folder)?.name ?? 'My files'}.</p>
    </div>
    <div class="modal-foot"><button type="button" class="btn" data-action="close-modal">Cancel</button><button type="submit" class="btn btn-primary">Create folder</button></div>
  </form>`, 'narrow');
}

/* ---------- The menu of a file or folder ---------- */

// Every file and folder has a ⋯ button that opens this menu. Right-clicking one, or pressing the
// context menu key on it, opens the same menu, so the button is there for people who never think
// of right-clicking a web page, and for phones and tablets, which can't.

const menuEl = $('#item-menu');
let menu = null; // { id, trigger } while the menu is open

function itemMenu(it) {
  const file = it.type === 'file';
  const fixed = it.id === 'requests'; // "My files" itself never shows up as an item
  return html`
    <button type="button" role="menuitem" data-action="open" data-id="${it.id}">${icon(file ? 'eye' : 'folder')}${file ? 'Preview' : 'Open'}</button>
    ${file ? html`<a role="menuitem" href="${store.contentUrl(it.id)}?download" download>${icon('download')}Download</a>` : ''}
    <button type="button" role="menuitem" data-action="rename" data-id="${it.id}" ${fixed ? raw('disabled') : ''}>${icon('pencil')}Rename…</button>
    ${file ? html`<button type="button" role="menuitem" data-action="share-files" data-ids="${it.id}">${icon('link')}Create share link…</button>` : ''}
    <div role="separator"></div>
    <button type="button" role="menuitem" class="danger" data-action="delete" data-ids="${it.id}" ${fixed ? raw('disabled') : ''}>${icon('trash')}Delete…</button>
    ${fixed ? html`<p class="menu-note">Stash keeps your upload links’ folders here, so this folder can’t be renamed or deleted.</p>` : ''}`;
}

const menuItems = () => [...menuEl.querySelectorAll('[role=menuitem]:not(:disabled)')];

/** Opens the menu below its ⋯ button, or at the mouse pointer after a right-click. */
function openMenu(id, trigger, point = null) {
  const it = store.item(id);
  if (!it) return;
  closeMenu();
  menuEl.innerHTML = itemMenu(it).s;
  menuEl.setAttribute('aria-label', `Actions for ${it.name}`);
  menuEl.hidden = false;
  const r = trigger.getBoundingClientRect();
  const { width, height } = menuEl.getBoundingClientRect();
  let x = point ? point.x : r.right - width;
  let y = point ? point.y : r.bottom + 4;
  if (y + height > innerHeight - 8) y = (point ? point.y : r.top - 4) - height;
  menuEl.style.left = `${Math.max(8, Math.min(x, innerWidth - width - 8))}px`;
  menuEl.style.top = `${Math.max(8, y)}px`;
  menu = { id, trigger };
  if (trigger.hasAttribute('aria-expanded')) trigger.setAttribute('aria-expanded', 'true');
  menuItems()[0]?.focus();
}

function closeMenu({ focus = false } = {}) {
  if (!menu) return;
  const { trigger } = menu;
  menu = null;
  menuEl.hidden = true;
  menuEl.innerHTML = '';
  if (trigger.hasAttribute('aria-expanded')) trigger.setAttribute('aria-expanded', 'false');
  if (focus && trigger.isConnected) trigger.focus();
}

// From the preview, the preview opens again after renaming, showing the new name.
function renameForm(id, reopen = false) {
  const it = store.item(id);
  if (!it) return;
  const isFile = it.type === 'file';
  const req = store.requestForFolder(it.id);
  showModal(html`<form data-submit="save-rename" data-id="${it.id}" data-reopen="${b(reopen)}">
    ${modalHead(isFile ? 'Rename file' : 'Rename folder')}
    <div class="modal-body">
      <div class="field"><label for="r-name">Name</label><input id="r-name" name="name" class="input" required maxlength="${req ? 120 : 255}" value="${it.name}" autocomplete="off" autofocus></div>
      ${req ? html`<p class="hint">The upload link gets the same name. People see it as the title of its upload page.</p>` : ''}
    </div>
    <div class="modal-foot"><button type="button" class="btn" data-action="close-modal">Cancel</button><button type="submit" class="btn btn-primary">Rename</button></div>
  </form>`, 'narrow');
  // Select the name without its extension, so typing replaces only that.
  const dot = it.name.lastIndexOf('.');
  modal.querySelector('#r-name').setSelectionRange(0, isFile && dot > 0 ? dot : it.name.length);
}

/* ---------- Uploading your own files ---------- */

// Each file gets its own row and progress bar in a panel in the bottom-right corner, and appears
// in its folder once it's uploaded. The server checks the free space, so a file that doesn't fit
// shows that message in its row.

const tray = $('#tray');
const uploads = [];
let uploadSeq = 0;
let trayCollapsed = false;

function addOwnerFiles(fileList) {
  const list = [...fileList];
  if (!list.length) return;
  const { section } = route();
  const target = section === 'files' && !state.query ? state.folder : 'root';
  for (const file of list) {
    const u = { id: ++uploadSeq, name: file.name, size: file.size, loaded: 0, status: 'uploading' };
    uploads.unshift(u);
    u.job = uploadFile(file, {
      base: '/api/uploads',
      fields: { parent: target },
      onProgress: (n) => { u.loaded = n; updateUploadRow(tray, u); },
    });
    u.job.done.then((item) => {
      Object.assign(u, { status: 'done', loaded: u.size, doneText: `${bytes(item.size)} · in ${store.item(item.parent)?.name ?? 'My files'}` });
      refresh();
    }, (err) => {
      if (err.status === -1) return; // cancelled, and the row is already gone
      if (err.status === 401) location.href = '/login';
      Object.assign(u, { status: 'error', error: err.message });
      renderTray();
    });
  }
  trayCollapsed = false;
  renderTray();
}

// Reload everything after uploads finish. Several finishing at once share one reload.
let loading = null;
let loadAgain = false;
function refresh() {
  if (loading) {
    loadAgain = true;
    return loading;
  }
  loading = (async () => {
    do {
      loadAgain = false;
      await store.load();
    } while (loadAgain);
    renderTray();
    render();
  })().catch(failedToLoad).finally(() => { loading = null; });
  return loading;
}

function renderTray() {
  if (!uploads.length) {
    tray.hidden = true;
    tray.innerHTML = '';
    return;
  }
  const busy = uploads.filter((u) => u.status === 'uploading').length;
  const done = uploads.filter((u) => u.status === 'done').length;
  const failed = uploads.length - busy - done;
  const title = [
    busy ? `Uploading ${plural(busy, 'file')}` : `${plural(done, 'upload')} complete`,
    busy && done ? `${done} done` : '',
    failed ? `${failed} failed` : '',
  ].filter(Boolean).join(' · ');
  tray.hidden = false;
  tray.classList.toggle('collapsed', trayCollapsed);
  tray.innerHTML = html`
    <div class="tray-head">
      <strong class="ellipsis">${title}</strong>
      <button type="button" class="icon-btn btn-ghost" data-action="tray-toggle" aria-expanded="${b(!trayCollapsed)}" aria-label="${trayCollapsed ? 'Show uploads' : 'Hide uploads'}" title="${trayCollapsed ? 'Show uploads' : 'Hide uploads'}">${icon(trayCollapsed ? 'chevron-up' : 'chevron-down')}</button>
      ${busy ? '' : html`<button type="button" class="icon-btn btn-ghost" data-action="tray-close" aria-label="Close" title="Close">${icon('x')}</button>`}
    </div>
    <div class="tray-body" aria-live="polite">${uploads.map((u) => uploadRow(u))}</div>`.s;
}

const filesIn = (it) => (it.type === 'file' ? [it] : store.children(it.id).flatMap(filesIn));

// Deleting moves things to the trash. The dialog says what goes, for how long it can come back,
// and what stops working meanwhile.
function confirmDelete(ids) {
  const its = ids.map((id) => store.item(id)).filter(Boolean);
  if (!its.length) return;
  const one = its.length === 1 ? its[0] : null;
  const files = new Set(its.flatMap(filesIn).map((f) => f.id));
  const many = !one || one.type === 'folder';
  const [pron, being] = many ? ['them', 'they’re'] : ['it', 'it’s'];
  const parts = [];
  if (one?.type === 'folder') {
    parts.push(files.size
      ? `The folder and the ${plural(files.size, 'file')} in it (${bytes(store.size(one.id))}) move to the trash.`
      : 'The folder moves to the trash.');
  } else {
    parts.push(`${one ? 'It moves' : 'They move'} to the trash.`);
  }
  parts.push(`You can restore ${pron} from there for ${plural(store.trashDays(), 'day')}. After that, ${being} deleted for good.`);
  const links = store.requests().filter((r) => !r.revoked && store.path(r.folder).some((p) => ids.includes(p.id)));
  if (links.length === 1) parts.push(`The upload link “${links[0].name}” stops working.`);
  else if (links.length) parts.push(`${links.length} upload links stop working.`);
  if (store.shares().some((s) => store.shareStatus(s) === 'active' && s.files.some((id) => files.has(id)))) {
    parts.push(`Share links leave ${pron} out while ${being} in the trash.`);
  }
  confirmModal({
    title: one ? `Delete “${one.name}”?` : `Delete ${plural(its.length, 'item')}?`,
    body: parts.join(' '),
    confirm: 'Delete',
    onConfirm: () => attempt(async () => {
      await store.moveToTrash(ids);
      ids.forEach((id) => state.selected.delete(id));
    }, `Moved ${one ? one.name : plural(its.length, 'item')} to the trash`),
  });
}

/* ---------- The trash ---------- */

function trashView() {
  const items = store.trash();
  const size = items.reduce((sum, it) => sum + store.contents(it.id).bytes, 0);
  return html`
    <div class="page-head">
      <div><h1>Trash</h1><p>Deleted files and folders stay here for ${plural(store.trashDays(), 'day')}. After that, they’re deleted for good.</p></div>
      ${items.length ? html`<button type="button" class="btn btn-danger" data-action="empty-trash">${icon('trash')} Empty trash</button>` : ''}
    </div>
    ${items.length
      ? html`<div class="table rows-trash">
          <div class="row head"><span>Name</span><span>Size</span><span>Deleted</span><span></span></div>
          ${items.map((it) => trashRow(it))}
        </div>
        <p class="hint trash-total">${plural(items.length, 'item')}, ${bytes(size)}. Until they’re deleted for good, they still take up space on the disk.</p>`
      : html`<div class="card empty">${icon('trash')}<p>The trash is empty.</p></div>`}`;
}

function trashRow(it) {
  const { files, bytes: size } = store.contents(it.id);
  const left = store.purgeAt(it) - Date.now();
  const gone = left <= 0 ? 'Deleted for good soon' : left < 864e5 ? 'Deleted for good within a day' : `Deleted for good in ${plural(Math.round(left / 864e5), 'day')}`;
  return html`<div class="row">
    <span class="name-cell"><span class="mini">${thumb(it)}</span><span class="min0 stack"><span class="ellipsis strong">${it.name}</span><span class="ellipsis muted small">From ${pathText(it.parent)}${it.type === 'folder' ? ` · ${plural(files, 'file')}` : ''}</span></span></span>
    <span class="muted nowrap"><span class="m-label">Size: </span>${bytes(size)}</span>
    <span class="stack"><span class="nowrap">${rel(it.trashed)}</span><span class="muted small nowrap">${gone}</span></span>
    <span class="row-actions">
      <button type="button" class="btn btn-sm" data-action="restore" data-id="${it.id}">${icon('undo')} Restore</button>
      <button type="button" class="icon-btn btn-ghost btn-danger" data-action="delete-for-good" data-id="${it.id}" aria-label="Delete ${it.name} for good" title="Delete for good">${icon('trash')}</button>
    </span>
  </div>`;
}

function restoreItem(id) {
  const it = store.item(id);
  if (!it) return;
  const where = store.item(it.parent)?.name ?? 'My files';
  const link = store.requests().some((r) => store.path(r.folder).some((p) => p.id === id));
  attempt(() => store.restore([id]), `Restored ${it.name} to ${where}.${link ? ' Its upload link stays revoked.' : ''}`);
}

function confirmDeleteForGood(id) {
  const it = store.item(id);
  if (!it) return;
  const { files, bytes: size } = store.contents(id);
  const link = store.requests().some((r) => store.path(r.folder).some((p) => p.id === id));
  confirmModal({
    title: `Delete “${it.name}” for good?`,
    body: [
      it.type === 'folder' && files ? `The folder and the ${plural(files, 'file')} in it (${bytes(size)}) are deleted.` : '',
      link ? 'Its upload link is deleted too.' : '',
      'This can’t be undone.',
    ].filter(Boolean).join(' '),
    confirm: 'Delete for good',
    onConfirm: () => attempt(() => store.deleteForGood([id]), `Deleted ${it.name} for good`),
  });
}

function confirmEmptyTrash() {
  const items = store.trash();
  const size = items.reduce((sum, it) => sum + store.contents(it.id).bytes, 0);
  confirmModal({
    title: 'Empty the trash?',
    body: `${items.length === 1 ? 'The item in it' : `The ${items.length} items in it`} (${bytes(size)}) ${items.length === 1 ? 'is' : 'are'} deleted for good. This can’t be undone.`,
    confirm: 'Empty trash',
    onConfirm: () => attempt(() => store.emptyTrash(), 'The trash is empty'),
  });
}

/* ---------- Upload links ---------- */

const FILTERS = [['all', 'All'], ['active', 'Active'], ['full', 'Full'], ['closed', 'Expired or revoked']];
const group = (status) => (status === 'expired' || status === 'revoked' ? 'closed' : status);

function requestList() {
  const all = store.requests();
  const counts = { all: all.length };
  for (const r of all) {
    const g = group(store.status(r));
    counts[g] = (counts[g] ?? 0) + 1;
  }
  const shown = state.linkFilter === 'all' ? all : all.filter((r) => group(store.status(r)) === state.linkFilter);
  return html`
    <div class="page-head">
      <div><h1>Upload links</h1><p>People with a link can send you files but can’t see anything else. Each link has its own size limit.</p></div>
      <button type="button" class="btn btn-primary" data-action="new-request">${icon('plus')} New upload link</button>
    </div>
    <div class="chips filters">${FILTERS.map(([key, label]) => html`<button type="button" class="chip" data-action="filter-links" data-filter="${key}" aria-pressed="${b(state.linkFilter === key)}">${label}<span class="count">${counts[key] ?? 0}</span></button>`)}</div>
    ${shown.length ? html`<div class="table rows-links">
      <div class="row head"><span>Name</span><span>Space used</span><span>Files</span><span>Expires</span><span>Status</span><span></span></div>
      ${shown.map((r) => requestRow(r))}
    </div>` : html`<div class="card empty">${icon('inbox')}<p>No upload links here.</p></div>`}`;
}

function requestRow(r) {
  const u = store.usage(r);
  return html`<div class="row link" role="button" tabindex="0" data-action="go" data-href="#/upload-links/${r.id}">
    <span class="name-cell"><span class="row-icon">${icon('inbox')}</span><span class="min0 stack"><span class="ellipsis strong">${r.name}</span><span class="ellipsis muted small">${r.password ? 'Password · ' : ''}Created ${rel(r.created)}</span></span></span>
    <span class="cell-wide usage">${meter(u.used, r.quota)}<span class="small"><span class="strong">${bytes(u.used)}</span> <span class="muted">of ${bytes(r.quota)}</span></span></span>
    <span class="muted"><span class="m-label">Files: </span>${u.files}${r.maxFiles ? ` / ${r.maxFiles}` : ''}</span>
    <span class="muted"><span class="m-label">Expires: </span>${expiresText(r.expires)}</span>
    <span>${badge(store.status(r))}</span>
    <span class="cell-actions"><button type="button" class="icon-btn btn-ghost" data-action="copy" data-text="${uploadUrl(r)}" data-msg="Link copied" aria-label="Copy link" title="Copy link">${icon('copy')}</button></span>
  </div>`;
}

function requestDetail(id) {
  const r = store.request(id);
  if (!r) return notFound('This upload link doesn’t exist.', '#/upload-links', 'upload links');
  const u = store.usage(r);
  const status = store.status(r);
  const closed = status === 'expired' || status === 'revoked';
  const files = store.children(r.folder);
  const url = uploadUrl(r);
  return html`
    <a class="back" href="#/upload-links">${icon('arrow-left')} Upload links</a>
    <div class="page-head">
      <div>
        <h1 class="title-row"><span class="ellipsis">${r.name}</span>${badge(status)}</h1>
        <p>Created ${date(r.created)} · ${store.inTrash(r.folder)
          ? html`its folder is in the <a href="#/trash">trash</a>`
          : html`files go to <a href="#/files/${r.folder}">${pathText(r.folder)}</a>`}</p>
      </div>
      <div class="actions">
        ${closed
          ? html`<button type="button" class="btn btn-primary" data-action="edit-request" data-id="${r.id}">Reopen…</button>`
          : html`<button type="button" class="btn" data-action="edit-request" data-id="${r.id}">Edit limits</button>
                 <button type="button" class="btn btn-danger" data-action="revoke-request" data-id="${r.id}">${icon('ban')} Revoke</button>`}
      </div>
    </div>
    <div class="stats">
      <div class="card stat">
        <div class="stat-label">Space used</div>
        <div class="stat-value">${bytes(u.used)} <span class="muted">of ${bytes(r.quota)}</span></div>
        ${meter(u.used, r.quota)}
        <p class="hint">${bytes(Math.max(0, r.quota - u.used))} left</p>
      </div>
      <div class="card stat">
        <div class="stat-label">Files received</div>
        <div class="stat-value">${u.files}${r.maxFiles ? html` <span class="muted">of ${r.maxFiles}</span>` : ''}</div>
        <p class="hint">${u.files ? `Last upload ${rel(u.last)}` : 'Nothing yet'}</p>
      </div>
      <div class="card stat">
        <div class="stat-label">${r.expires && r.expires <= Date.now() ? 'Expired' : 'Expires'}</div>
        <div class="stat-value">${r.expires ? date(r.expires) : 'Never'}</div>
        <p class="hint">${r.expires ? rel(r.expires) : 'No expiry date'}</p>
      </div>
      <div class="card stat">
        <div class="stat-label">Access</div>
        <div class="stat-value">${r.password ? 'Password' : 'Link only'}</div>
        <p class="hint">${r.password ? 'Uploaders need the password' : 'Anyone with the link can upload'}</p>
      </div>
    </div>
    ${r.message ? html`<div class="card note"><div class="stat-label">Message shown to uploaders</div><p>${r.message}</p></div>` : ''}
    ${closed ? '' : html`<section class="section"><h2>Send this link</h2><div class="card">${linkPanel(url, r.name, uploadSnippets(url, r), `/d/${r.token}`)}</div></section>`}
    <section class="section">
      <h2>Received files ${u.files ? html`<span class="muted">· ${bytes(u.used)}</span>` : ''}</h2>
      ${files.length ? listView(files, { selectable: false }) : html`<div class="card empty">${icon('inbox')}<p>Nothing uploaded yet.</p></div>`}
    </section>`;
}

const QUOTAS = [[25 * MB, '25 MB'], [100 * MB, '100 MB'], [GB, '1 GB'], [10 * GB, '10 GB'], [100 * GB, '100 GB'], [500 * GB, '500 GB']];
const EXPIRY = [[1, 'In 1 day'], [7, 'In 7 days'], [30, 'In 30 days'], [90, 'In 90 days'], [0, 'Never']];

function splitBytes(n) {
  const unit = n >= TB ? TB : n >= GB ? GB : MB;
  return [Math.round((n / unit) * 100) / 100, unit];
}

function expirySelect(id, { keep = null, fallback = 7 } = {}) {
  return html`<select id="${id}" name="expires" class="input">
    ${keep ? html`<option value="keep" selected>${keep}</option>` : ''}
    ${EXPIRY.map(([days, label]) => html`<option value="${days}" ${!keep && days === fallback ? raw('selected') : ''}>${label}</option>`)}
  </select>`;
}

// A link's password is stored hashed, so editing can only keep, replace or remove it.
function passwordField(existing = false) {
  return html`<div class="field">
    <label class="check-row"><input type="checkbox" name="usePassword" data-action="toggle-password" data-existing="${b(existing)}" ${existing ? raw('checked') : ''}> Require a password</label>
    <input name="password" class="input" type="text" autocomplete="off" placeholder="${existing ? 'Leave empty to keep the current password' : 'Password to give the recipient'}" ${existing ? '' : raw('hidden')}>
  </div>`;
}

/** What to send for the password: a new one, '' to remove it, or undefined to leave it as it is. */
function passwordChange(f) {
  const existing = f.usePassword.dataset.existing === 'true';
  if (!f.usePassword.checked) return existing ? '' : undefined;
  return f.password.value.trim() || undefined;
}

function setBusy(form, busy) {
  const button = form.querySelector('[type=submit]');
  if (button) button.disabled = busy;
}

function requestForm(r = null) {
  const quota = r?.quota ?? GB;
  const preset = QUOTAS.some(([v]) => v === quota);
  const [amount, unit] = preset ? ['', GB] : splitBytes(quota);
  const expired = r?.expires && r.expires <= Date.now();
  const keep = r && !expired ? (r.expires ? `Keep (${date(r.expires)})` : 'Keep (never)') : null;
  showModal(html`<form data-submit="save-request" data-id="${r?.id ?? ''}">
    ${modalHead(!r ? 'New upload link' : expired || r.revoked ? 'Reopen upload link' : 'Edit upload link')}
    <div class="modal-body">
      <div class="field">
        <label for="f-name">Name</label>
        <input id="f-name" name="name" class="input" required maxlength="120" value="${r?.name ?? ''}" placeholder="e.g. Acme — database export" autofocus>
        <p class="hint">Shown as the title on the upload page.</p>
      </div>
      <fieldset class="field">
        <legend>Size limit</legend>
        <div class="chips">
          ${QUOTAS.map(([v, label]) => html`<button type="button" class="chip" data-action="pick-quota" data-value="${v}" aria-pressed="${b(preset && v === quota)}">${label}</button>`)}
          <button type="button" class="chip" data-action="pick-quota" data-value="custom" aria-pressed="${b(!preset)}">Custom</button>
        </div>
        <div class="custom-quota" ${preset ? raw('hidden') : ''}>
          <input class="input" type="number" name="amount" min="0" step="any" value="${amount}" placeholder="250" aria-label="Custom size">
          <select class="input" name="unit" aria-label="Unit">${[[MB, 'MB'], [GB, 'GB'], [TB, 'TB']].map(([v, label]) => html`<option value="${v}" ${v === unit ? raw('selected') : ''}>${label}</option>`)}</select>
        </div>
        <input type="hidden" name="quota" value="${preset ? quota : 'custom'}">
        <p class="hint">The total for everything uploaded through this link. Uploads stop once it’s reached.</p>
      </fieldset>
      <div class="two">
        <div class="field"><label for="f-exp">Expires</label>${expirySelect('f-exp', { keep })}</div>
        <div class="field"><label for="f-max">Max files <span class="muted">· optional</span></label><input id="f-max" name="maxFiles" type="number" min="1" step="1" class="input" value="${r?.maxFiles ?? ''}" placeholder="No limit"></div>
      </div>
      ${passwordField(r?.password)}
      <div class="field"><label for="f-msg">Message for uploaders <span class="muted">· optional</span></label><textarea id="f-msg" name="message" class="input" rows="3" maxlength="500" placeholder="What should they upload?">${r?.message ?? ''}</textarea></div>
    </div>
    <div class="modal-foot"><button type="button" class="btn" data-action="close-modal">Cancel</button><button type="submit" class="btn btn-primary">${r ? 'Save' : 'Create link'}</button></div>
  </form>`);
}

function linkSummary(r) {
  return [
    `Up to ${bytes(r.quota)}`,
    r.maxFiles && `${r.maxFiles} files max`,
    r.expires ? `expires ${date(r.expires)}` : 'never expires',
    r.password && 'password protected',
  ].filter(Boolean).join(' · ');
}

function showCreatedRequest(r) {
  const url = uploadUrl(r);
  showModal(html`${modalHead('Upload link ready')}
    <div class="modal-body">
      <div class="banner">${icon('check')}<div><strong>${r.name}</strong><div class="small muted">${linkSummary(r)}</div></div></div>
      ${linkPanel(url, r.name, uploadSnippets(url, r), `/d/${r.token}`)}
      <p class="hint">Opened in a browser, the link shows an upload page. The commands upload straight from a terminal on macOS, Linux or Windows, which is handy for very large files. For several files at once: curl -T "{a.zip,b.zip}" followed by the link.</p>
    </div>
    <div class="modal-foot"><a class="btn" href="#/upload-links/${r.id}" data-action="close-modal">View link details</a><button type="button" class="btn btn-primary" data-action="close-modal">Done</button></div>`);
}

/* ---------- Share links ---------- */

function shareList() {
  const all = store.shares();
  return html`
    <div class="page-head">
      <div><h1>Share links</h1><p>Send someone a set of files. They can view and download only what you picked.</p></div>
      <button type="button" class="btn btn-primary" data-action="new-share">${icon('plus')} New share link</button>
    </div>
    ${all.length ? html`<div class="table rows-shares">
      <div class="row head"><span>Name</span><span>Files</span><span>Downloads</span><span>Expires</span><span>Status</span><span></span></div>
      ${all.map((s) => shareRow(s))}
    </div>` : html`<div class="card empty">${icon('link')}<p>No share links yet.</p></div>`}`;
}

function shareRow(s) {
  const files = store.shareFiles(s);
  return html`<div class="row link" role="button" tabindex="0" data-action="go" data-href="#/share-links/${s.id}">
    <span class="name-cell"><span class="stack-thumbs">${files.slice(0, 3).map((f) => html`<span class="mini">${thumb(f)}</span>`)}</span><span class="min0 stack"><span class="ellipsis strong">${s.name}</span><span class="ellipsis muted small">${s.password ? 'Password · ' : ''}Created ${rel(s.created)}</span></span></span>
    <span class="muted"><span class="m-label">Files: </span>${files.length} · ${bytes(total(files))}</span>
    <span class="muted"><span class="m-label">Downloads: </span>${s.downloads}${s.maxDownloads ? ` / ${s.maxDownloads}` : ''}</span>
    <span class="muted"><span class="m-label">Expires: </span>${expiresText(s.expires)}</span>
    <span>${badge(store.shareStatus(s), 'Limit reached')}</span>
    <span class="cell-actions"><button type="button" class="icon-btn btn-ghost" data-action="copy" data-text="${shareUrl(s)}" data-msg="Link copied" aria-label="Copy link" title="Copy link">${icon('copy')}</button></span>
  </div>`;
}

function shareDetail(id) {
  const s = store.share(id);
  if (!s) return notFound('This share link doesn’t exist.', '#/share-links', 'share links');
  const files = store.shareFiles(s);
  const trashedFiles = store.sharedInTrash(s);
  const status = store.shareStatus(s);
  const url = shareUrl(s);
  return html`
    <a class="back" href="#/share-links">${icon('arrow-left')} Share links</a>
    <div class="page-head">
      <div>
        <h1 class="title-row"><span class="ellipsis">${s.name}</span>${badge(status, 'Limit reached')}</h1>
        <p>Created ${date(s.created)}</p>
      </div>
      <div class="actions">
        ${status === 'active'
          ? html`<button type="button" class="btn btn-danger" data-action="revoke-share" data-id="${s.id}">${icon('ban')} Revoke</button>`
          : html`<button type="button" class="btn btn-primary" data-action="reopen-share" data-id="${s.id}">Reopen for 7 days</button>`}
      </div>
    </div>
    <div class="stats">
      <div class="card stat"><div class="stat-label">Files</div><div class="stat-value">${files.length}</div><p class="hint">${bytes(total(files))} in total</p></div>
      <div class="card stat"><div class="stat-label">Downloads</div><div class="stat-value">${s.downloads}${s.maxDownloads ? html` <span class="muted">of ${s.maxDownloads}</span>` : ''}</div><p class="hint">${s.maxDownloads ? `Stops after ${s.maxDownloads}` : 'No limit'}</p></div>
      <div class="card stat"><div class="stat-label">${s.expires && s.expires <= Date.now() ? 'Expired' : 'Expires'}</div><div class="stat-value">${s.expires ? date(s.expires) : 'Never'}</div><p class="hint">${s.expires ? rel(s.expires) : 'No expiry date'}</p></div>
      <div class="card stat"><div class="stat-label">Access</div><div class="stat-value">${s.password ? 'Password' : 'Link only'}</div><p class="hint">${s.password ? 'Visitors need the password' : 'Anyone with the link can view'}</p></div>
    </div>
    ${s.message ? html`<div class="card note"><div class="stat-label">Message shown to visitors</div><p>${s.message}</p></div>` : ''}
    ${status === 'active' ? html`<section class="section"><h2>Send this link</h2><div class="card">${linkPanel(url, s.name, shareSnippets(url, s), `/s/${s.token}`)}</div></section>` : ''}
    <section class="section">
      <h2>Files in this share</h2>
      ${trashedFiles ? html`<p class="hint section-hint">${trashedFiles === 1 ? '1 file is' : `${trashedFiles} files are`} in the <a href="#/trash">trash</a>, so visitors don’t see ${trashedFiles === 1 ? 'it' : 'them'}. Restore ${trashedFiles === 1 ? 'it' : 'them'} to share ${trashedFiles === 1 ? 'it' : 'them'} again.</p>` : ''}
      ${files.length ? html`<div class="grid">${files.map((f) => tile(f, false))}</div>` : html`<div class="card empty">${icon('file')}<p>All files in this share were deleted.</p></div>`}
    </section>`;
}

function defaultShareName(ids) {
  const files = ids.map((id) => store.item(id)).filter(Boolean);
  if (files.length === 1) return files[0].name.replace(/\.[^.]+$/, '');
  const parents = new Set(files.map((f) => f.parent));
  if (files.length && parents.size === 1 && !parents.has('root')) return store.item([...parents][0]).name;
  return '';
}

const pickLabel = (n, size) => (n ? `· ${n} selected, ${bytes(size)}` : '· none selected');

function shareForm(ids = []) {
  const chosen = new Set(ids);
  const all = store.files();
  const list = [...all.filter((f) => chosen.has(f.id)), ...all.filter((f) => !chosen.has(f.id)).sort((a, b) => a.name.localeCompare(b.name))];
  const name = defaultShareName(ids);
  showModal(html`<form data-submit="save-share">
    ${modalHead('New share link')}
    <div class="modal-body">
      <div class="field">
        <label for="s-name">Name</label>
        <input id="s-name" name="name" class="input" required maxlength="120" value="${name}" placeholder="e.g. Photos from Saturday" autofocus>
        <p class="hint">Shown as the title on the download page.</p>
      </div>
      <fieldset class="field picker">
        <legend>Files <span class="muted" data-pick-count>${pickLabel(chosen.size, total(all.filter((f) => chosen.has(f.id))))}</span></legend>
        <div class="input-icon">${icon('search')}<input type="search" class="input" placeholder="Filter files" data-filter="pick" aria-label="Filter files"></div>
        <div class="pick-list">${list.map((f) => html`<label class="pick" data-name="${f.name.toLowerCase()}">
          <input type="checkbox" name="files" value="${f.id}" ${chosen.has(f.id) ? raw('checked') : ''}>
          <span class="mini">${thumb(f)}</span>
          <span class="min0 stack"><span class="ellipsis">${f.name}</span><span class="ellipsis muted small">${pathText(f.parent)}</span></span>
          <span class="muted small nowrap">${bytes(f.size)}</span>
        </label>`)}</div>
      </fieldset>
      <div class="two">
        <div class="field"><label for="s-exp">Expires</label>${expirySelect('s-exp', { fallback: 30 })}</div>
        <div class="field"><label for="s-max">Max downloads <span class="muted">· optional</span></label><input id="s-max" name="maxDownloads" type="number" min="1" step="1" class="input" placeholder="No limit"><p class="hint">Every file viewed or downloaded counts. A limited share shows no previews.</p></div>
      </div>
      ${passwordField()}
      <div class="field"><label for="s-msg">Message <span class="muted">· optional</span></label><textarea id="s-msg" name="message" class="input" rows="2" maxlength="500" placeholder="Shown above the files"></textarea></div>
    </div>
    <div class="modal-foot"><button type="button" class="btn" data-action="close-modal">Cancel</button><button type="submit" class="btn btn-primary">Create link</button></div>
  </form>`);
}

function showCreatedShare(s) {
  const url = shareUrl(s);
  const files = store.shareFiles(s);
  showModal(html`${modalHead('Share link ready')}
    <div class="modal-body">
      <div class="banner">${icon('check')}<div><strong>${s.name}</strong><div class="small muted">${[plural(files.length, 'file'), bytes(total(files)), s.expires ? `expires ${date(s.expires)}` : 'never expires', s.password && 'password protected'].filter(Boolean).join(' · ')}</div></div></div>
      ${linkPanel(url, s.name, shareSnippets(url, s), `/s/${s.token}`)}
    </div>
    <div class="modal-foot"><a class="btn" href="#/share-links/${s.id}" data-action="close-modal">View link details</a><button type="button" class="btn btn-primary" data-action="close-modal">Done</button></div>`);
}

/* ---------- Events ---------- */

const actions = {
  menu: () => document.body.classList.toggle('nav-open'),
  nav: (el) => {
    document.body.classList.remove('nav-open');
    if (state.query) {
      clearSearch();
      if (el.getAttribute('href') === location.hash) render();
    }
  },
  go: (el) => navigate(el.dataset.href),
  'item-menu': (el) => {
    if (menu?.trigger === el) closeMenu();
    else openMenu(el.dataset.id, el);
  },
  open: (el) => {
    const it = store.item(el.dataset.id);
    if (!it) return;
    if (it.type === 'folder') navigate(`#/files/${it.id}`);
    else openPreview(it.id);
  },
  select: (el) => {
    if (el.checked) state.selected.add(el.dataset.id);
    else state.selected.delete(el.dataset.id);
    render();
  },
  'clear-selection': () => { state.selected.clear(); render(); },
  layout: (el) => { state.layout = el.dataset.layout; save('layout', state.layout); render(); },
  'remove-upload': (el) => {
    const i = uploads.findIndex((u) => u.id === Number(el.dataset.id));
    if (i >= 0) {
      if (uploads[i].status === 'uploading') uploads[i].job.cancel();
      uploads.splice(i, 1);
    }
    renderTray();
  },
  'tray-toggle': () => { trayCollapsed = !trayCollapsed; renderTray(); },
  'tray-close': () => { uploads.length = 0; renderTray(); },
  'new-folder': () => folderForm(),
  rename: (el) => renameForm(el.dataset.id, el.dataset.reopen === 'true'),
  logout: async () => {
    await request('POST', '/api/logout').catch(() => {});
    location.href = '/login';
  },
  'share-files': (el) => shareForm(el.dataset.ids.split(',')),
  'share-selected': () => shareForm([...state.selected]),
  delete: (el) => confirmDelete(el.dataset.ids.split(',')),
  'delete-selected': () => confirmDelete([...state.selected]),
  'close-modal': () => modal.close(),
  // A click anywhere on the link copies it, and selects it to show what was copied.
  'copy-link': (el) => {
    const input = el.closest('.link-field').querySelector('input');
    input.select();
    copy(input.value, 'Link copied');
  },
  'toggle-qr': (el) => {
    const panel = el.closest('.link-panel').querySelector('[data-qr]');
    panel.hidden = !panel.hidden;
    el.setAttribute('aria-expanded', b(!panel.hidden));
  },
  'download-qr': (el) => downloadQr(el.dataset.url, `${slug(el.dataset.name)}-qr-code.png`),
  confirm: () => {
    const fn = pendingConfirm;
    pendingConfirm = null;
    modal.close();
    fn?.();
  },
  'clear-search': () => { clearSearch(); render(); },
  'filter-links': (el) => { state.linkFilter = el.dataset.filter; render(); },
  'new-request': () => requestForm(),
  'edit-request': (el) => requestForm(store.request(el.dataset.id)),
  'revoke-request': (el) => confirmModal({
    title: 'Revoke this upload link?',
    body: 'The link stops working right away. Files already uploaded stay in your files.',
    confirm: 'Revoke link',
    onConfirm: () => attempt(() => store.updateRequest(el.dataset.id, { revoked: true }), 'Upload link revoked'),
  }),
  'pick-quota': (el) => {
    const fieldset = el.closest('fieldset');
    fieldset.querySelectorAll('.chip').forEach((c) => c.setAttribute('aria-pressed', b(c === el)));
    fieldset.querySelector('[name=quota]').value = el.dataset.value;
    const box = fieldset.querySelector('.custom-quota');
    box.hidden = el.dataset.value !== 'custom';
    if (!box.hidden) box.querySelector('input').focus();
  },
  'toggle-password': (el) => {
    const input = el.form.elements.password;
    input.hidden = !el.checked;
    input.required = el.checked && el.dataset.existing !== 'true';
    if (el.checked) input.focus();
  },
  'new-share': () => shareForm([...state.selected]),
  'revoke-share': (el) => confirmModal({
    title: 'Revoke this share link?',
    body: 'People with the link can no longer view or download these files. The files stay in your files.',
    confirm: 'Revoke link',
    onConfirm: () => attempt(() => store.updateShare(el.dataset.id, { revoked: true }), 'Share link revoked'),
  }),
  restore: (el) => restoreItem(el.dataset.id),
  'delete-for-good': (el) => confirmDeleteForGood(el.dataset.id),
  'empty-trash': () => confirmEmptyTrash(),
  'reopen-share': (el) => attempt(() => store.updateShare(el.dataset.id, { revoked: false, expiresDays: 7, maxDownloads: 0 }), 'Share link reopened for 7 days'),
};

const submits = {
  'save-request': async (form) => {
    const f = form.elements;
    const quota = f.quota.value === 'custom' ? Number(f.amount.value) * Number(f.unit.value) : Number(f.quota.value);
    if (!(quota > 0)) return invalid(f.amount, 'Enter a size for this link');
    const data = {
      name: f.name.value.trim(),
      quota: Math.round(quota),
      maxFiles: Number(f.maxFiles.value) || 0,
      password: passwordChange(f),
      message: f.message.value.trim(),
    };
    if (f.expires.value !== 'keep') data.expiresDays = Number(f.expires.value);

    setBusy(form, true);
    if (form.dataset.id) {
      if (await attempt(() => store.updateRequest(form.dataset.id, { ...data, revoked: false }), 'Upload link saved')) modal.close();
    } else {
      const r = await attempt(() => store.createRequest(data));
      if (r) showCreatedRequest(r);
    }
    setBusy(form, false);
  },
  'save-share': async (form) => {
    const f = form.elements;
    const ids = [...form.querySelectorAll('input[name=files]:checked')].map((i) => i.value);
    if (!ids.length) {
      form.querySelector('.picker').classList.add('invalid');
      toast('Pick at least one file', 'alert');
      return;
    }
    setBusy(form, true);
    const s = await attempt(() => store.createShare({
      name: f.name.value.trim(),
      files: ids,
      expiresDays: Number(f.expires.value),
      maxDownloads: Number(f.maxDownloads.value) || 0,
      password: passwordChange(f),
      message: f.message.value.trim(),
    }));
    setBusy(form, false);
    if (s) {
      state.selected.clear();
      render();
      showCreatedShare(s);
    }
  },
  'save-rename': async (form) => {
    const { id } = form.dataset;
    const input = form.elements.name;
    const name = input.value.trim();
    const done = () => (form.dataset.reopen === 'true' ? openPreview(id) : modal.close());
    if (/[/\\]/.test(name)) return invalid(input, 'Names can’t contain / or \\.');
    if (name === store.item(id)?.name) return done();
    setBusy(form, true);
    if (await attempt(() => store.rename(id, name), `Renamed to ${name}`)) done();
    setBusy(form, false);
  },
  'save-folder': async (form) => {
    setBusy(form, true);
    const name = form.elements.name.value.trim();
    if (await attempt(() => store.createFolder(state.folder, name), `Created ${name}`)) modal.close();
    setBusy(form, false);
  },
};

hydrateIcons();
delegate(actions, submits);

window.addEventListener('hashchange', () => {
  state.selected.clear();
  clearSearch();
  if (modal.open) modal.close();
  render();
  window.scrollTo(0, 0);
});

searchInput.addEventListener('input', () => {
  state.query = searchInput.value.trim();
  render();
});
searchInput.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { clearSearch(); render(); }
});
document.addEventListener('keydown', (e) => {
  if (e.key === '/' && !modal.open && !e.target.closest('input, textarea, select')) {
    e.preventDefault();
    searchInput.focus();
  }
});

modal.addEventListener('click', (e) => { if (e.target === modal) modal.close(); });

// The menu of a file or folder: right-click opens it, and any click closes it again, after the
// chosen action ran. The ⋯ buttons open and close it themselves.
document.addEventListener('contextmenu', (e) => {
  const el = e.target.closest('#view [data-action="open"][data-id]');
  if (!el) return;
  e.preventDefault();
  // The context menu key fires this too, without a pointer over the item: open it below the item then.
  const r = el.getBoundingClientRect();
  const pointer = e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
  openMenu(el.dataset.id, el, pointer ? { x: e.clientX, y: e.clientY } : null);
});
document.addEventListener('click', (e) => {
  if (menu && !e.target.closest('[data-action="item-menu"]')) closeMenu();
});
menuEl.addEventListener('keydown', (e) => {
  const items = menuItems();
  const i = items.indexOf(document.activeElement);
  const n = items.length;
  const go = { ArrowDown: (i + 1) % n, ArrowUp: (i - 1 + n) % n, Home: 0, End: n - 1 }[e.key];
  if (go !== undefined) {
    e.preventDefault();
    items[go]?.focus();
  } else if (e.key === 'Escape' || e.key === 'Tab') {
    e.preventDefault();
    closeMenu({ focus: true });
  }
});
window.addEventListener('scroll', () => closeMenu(), { capture: true, passive: true });
window.addEventListener('resize', () => closeMenu());

// Share form: live file count and filter.
document.addEventListener('change', (e) => {
  if (e.target.name !== 'files' || !e.target.form) return;
  const form = e.target.form;
  const checked = [...form.querySelectorAll('input[name=files]:checked')];
  form.querySelector('[data-pick-count]').textContent = pickLabel(checked.length, total(checked.map((i) => store.item(i.value))));
  form.querySelector('.picker').classList.remove('invalid');
});
document.addEventListener('input', (e) => {
  if (!e.target.matches('[data-filter="pick"]')) return;
  const q = e.target.value.trim().toLowerCase();
  e.target.closest('.picker').querySelectorAll('.pick').forEach((el) => { el.hidden = Boolean(q) && !el.dataset.name.includes(q); });
});

// Upload with the drop area's button, or by dropping files anywhere on a folder's page.
document.addEventListener('change', (e) => {
  if (!e.target.matches('[data-file-input]')) return;
  addOwnerFiles(e.target.files);
  e.target.value = '';
});

const canDropHere = () => route().section === 'files' && !state.query && !modal.open;

document.addEventListener('dragover', (e) => {
  if (!e.dataTransfer?.types.includes('Files')) return;
  e.preventDefault(); // otherwise the browser opens the dropped file and leaves the app
  if (canDropHere()) {
    $('.drop-hint').textContent = `Drop to upload to ${store.item(state.folder).name}`;
    document.body.classList.add('dropping');
  } else {
    e.dataTransfer.dropEffect = 'none';
  }
});
document.addEventListener('dragleave', (e) => {
  if (!e.relatedTarget) document.body.classList.remove('dropping');
});
document.addEventListener('drop', (e) => {
  document.body.classList.remove('dropping');
  if (!e.dataTransfer?.files.length) return;
  e.preventDefault();
  if (canDropHere()) addOwnerFiles(e.dataTransfer.files);
});


// Leaving the page would stop uploads that are still running.
window.addEventListener('beforeunload', (e) => {
  if (uploads.some((u) => u.status === 'uploading')) e.preventDefault();
});

store.load().then(render, failedToLoad);

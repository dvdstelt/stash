// Public upload page: what someone sees when they open an upload link (/d/<token>).
// They can add files up to the link's limits, and never see anything else.

const {
  html, icon, bytes, date, uploadSnippets, snippetTabs, uploadRow, updateUploadRow, dropZone,
  pickedFiles, droppedFiles, delegate, brandHeader, stateCard, passwordGate,
} = window.StashUI;
const { request, uploadFile } = window.StashAPI;

const page = document.getElementById('page');
const token = decodeURIComponent(location.pathname.split('/')[2] ?? '');
const api = `/api/public/d/${encodeURIComponent(token)}`;
const uploads = [];
let info = null; // what the server says about the link
let seq = 0;

async function loadInfo() {
  try {
    info = await request('GET', api);
  } catch (err) {
    info = err.body?.status ? err.body : { status: 'error', error: err.message };
  }
  render();
}

function render() {
  let body;
  if (!info) body = html`<section class="card state"><p class="muted">Loading…</p></section>`;
  else if (info.status === 'gone') body = stateCard('ban', 'This link doesn’t work', 'It may be mistyped, or the person who sent it turned it off. Ask them for a new link.');
  else if (info.status === 'expired') body = stateCard('clock', 'This upload link has expired', `It stopped accepting files${info.expires ? ` on ${date(info.expires)}` : ''}. If you still need to send something, ask for a new link.`);
  else if (info.status === 'error') body = stateCard('alert', 'Couldn’t open this link', `${info.error} Try again in a moment.`);
  else if (info.locked) body = passwordGate(info.name, 'This upload link is password protected. Enter the password you were given.');
  else if (info.status === 'full' && !uploads.length) body = stateCard('drive', 'This upload link is full', `${fullReason()} Ask the person who sent it if you need to upload more.`);
  else body = uploader();
  page.innerHTML = html`${brandHeader()}${body}`.s;
  page.querySelector('[autofocus]')?.focus();
}

function fullReason() {
  return info.maxFiles && info.files >= info.maxFiles
    ? `It already has the maximum of ${info.maxFiles} files.`
    : `All ${bytes(info.quota)} of space has been used.`;
}

/** Space and file slots left, counting this visitor's uploads that are still running. */
function room() {
  const busy = uploads.filter((u) => u.status === 'uploading');
  return {
    left: Math.max(0, info.quota - info.used - busy.reduce((sum, u) => sum + u.size, 0)),
    filesLeft: info.maxFiles ? Math.max(0, info.maxFiles - info.files - busy.length) : Infinity,
  };
}

function uploader() {
  const { left, filesLeft } = room();
  const pct = Math.min(100, ((info.quota - left) / info.quota) * 100);
  const blocked = left <= 0 || filesLeft <= 0;
  const done = uploads.filter((u) => u.status === 'done').length;
  return html`
    <section class="card intro">
      <h1>${info.name}</h1>
      <p class="lede">You’ve been asked to upload files. Only the person who sent you this link can see them.</p>
      ${info.message ? html`<blockquote class="message">${info.message}</blockquote>` : ''}
      <div class="room">
        <div class="room-head"><span><strong>${bytes(left)}</strong> left</span><span class="muted small">of ${bytes(info.quota)}</span></div>
        <div class="meter${pct >= 100 ? ' full' : pct >= 80 ? ' warn' : ''}"><span style="width:${pct.toFixed(1)}%"></span></div>
        <ul class="facts">
          <li>${icon('clock', 'sm')} ${info.expires ? `Open until ${date(info.expires)}` : 'No expiry date'}</li>
          ${info.maxFiles ? html`<li>${icon('file', 'sm')} ${filesLeft} of ${info.maxFiles} files left</li>` : ''}
        </ul>
      </div>
    </section>

    ${blocked
      ? dropZone({ title: 'This link is full', hint: 'You can’t add more files.', disabled: true })
      : dropZone({ title: 'Drag files or folders here', hint: `Any kind of file · up to ${bytes(left)} in total` })}

    ${uploads.length ? html`<section class="card uploads" aria-live="polite">
      <h2>Your uploads${done ? ` · ${done} of ${uploads.filter((u) => u.status !== 'error').length} done` : ''}</h2>
      ${uploads.map((u) => uploadRow(u))}
    </section>` : ''}

    <details class="card cli">
      <summary>${icon('terminal')} Upload from the command line <span class="muted">· for very large files or servers</span></summary>
      <div class="cli-body">
        <p>Works on macOS, Linux and Windows 10 or later. Each command uploads one file and keeps its name; replace <code>export.sql</code> with your file. To send several at once with curl, use <code>-T "{a.zip,b.zip}"</code>.</p>
        ${snippetTabs(uploadSnippets(`${location.origin}/d/${token}`, info))}
      </div>
    </details>

    <p class="fine">${icon('lock', 'xs')} You can only upload here. You can’t see files anyone else has uploaded.</p>`;
}

/** Starts uploading files from pickedFiles or droppedFiles, each into the folder it was in. */
function add(files) {
  for (const { file, path, name, error } of files) {
    const { left, filesLeft } = room();
    const u = { id: ++seq, name: file?.name ?? name, path, size: file?.size ?? 0, loaded: 0, status: 'uploading' };
    uploads.unshift(u);
    if (error) {
      Object.assign(u, { status: 'error', error });
    } else if (filesLeft <= 0) {
      Object.assign(u, { status: 'error', error: `This link takes at most ${info.maxFiles} files.` });
    } else if (file.size > left) {
      Object.assign(u, { status: 'error', error: left ? `Too large. Only ${bytes(left)} is left on this link.` : 'There’s no space left on this link.' });
    } else {
      start(u, file);
    }
  }
  render();
}

function start(u, file) {
  u.job = uploadFile(file, { base: `${api}/uploads`, fields: { path: u.path }, onProgress: (n) => { u.loaded = n; updateUploadRow(page, u); } });
  u.job.done.then((item) => {
    Object.assign(u, { status: 'done', loaded: u.size });
    info.used += item.size;
    info.files += 1;
    render();
  }, (err) => {
    if (err.status === -1) return; // cancelled, and the row is already gone
    Object.assign(u, { status: 'error', error: err.message });
    render();
  });
}

delegate({
  'remove-upload': (el) => {
    const i = uploads.findIndex((u) => u.id === Number(el.dataset.id));
    if (i >= 0) {
      if (uploads[i].status === 'uploading') uploads[i].job.cancel();
      uploads.splice(i, 1);
    }
    render();
  },
}, {
  unlock: async (form) => {
    const button = form.querySelector('button');
    button.disabled = true;
    try {
      await request('POST', `${api}/unlock`, { password: form.elements.pw.value });
      await loadInfo();
    } catch (err) {
      const box = page.querySelector('[data-error]');
      box.textContent = err.message;
      box.hidden = false;
      button.disabled = false;
      form.elements.pw.select();
    }
  },
});

page.addEventListener('change', (e) => {
  if (e.target.matches('[data-file-input]')) add(pickedFiles(e.target));
});
for (const type of ['dragenter', 'dragover']) {
  page.addEventListener(type, (e) => {
    const zone = e.target.closest('[data-dropzone]:not(.disabled)');
    if (!zone) return;
    e.preventDefault();
    zone.classList.add('drag');
  });
}
page.addEventListener('dragleave', (e) => {
  const zone = e.target.closest('[data-dropzone]');
  if (zone && !zone.contains(e.relatedTarget)) zone.classList.remove('drag');
});
page.addEventListener('drop', (e) => {
  const zone = e.target.closest('[data-dropzone]:not(.disabled)');
  if (!zone) return;
  e.preventDefault();
  zone.classList.remove('drag');
  droppedFiles(e.dataTransfer).then(add);
});
// A drop that misses the drop zone shouldn't open the file in the browser.
window.addEventListener('dragover', (e) => e.preventDefault());
window.addEventListener('drop', (e) => e.preventDefault());
// Leaving the page would stop uploads that are still running.
window.addEventListener('beforeunload', (e) => {
  if (uploads.some((u) => u.status === 'uploading')) e.preventDefault();
});

render();
loadInfo();

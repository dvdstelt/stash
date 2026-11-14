// Public share page: what someone sees when they open a share link (/s/<token>).

const {
  html, icon, bytes, date, slug, kindOf, thumb, previewMedia, shareSnippets, snippetTabs,
  delegate, brandHeader, stateCard, passwordGate,
} = window.StashUI;
const { request } = window.StashAPI;

const page = document.getElementById('page');
const lightbox = document.getElementById('lightbox');
const token = decodeURIComponent(location.pathname.split('/')[2] ?? '');
const base = `/s/${encodeURIComponent(token)}`;
const api = `/api/public/s/${encodeURIComponent(token)}`;
const PREVIEWABLE = new Set(['image', 'pdf', 'video']);
const TEXT_PREVIEW = 256 * 1024;
let info = null;
let files = [];
let current = 0;

const fileUrl = (f, download = false) => `${base}/files/${encodeURIComponent(f.id)}${download ? '?download' : ''}`;
const folderText = (path) => path.split('/').join(' / ');

async function loadInfo() {
  try {
    info = await request('GET', api);
  } catch (err) {
    info = err.body?.status ? err.body : { status: 'error', error: err.message };
  }
  // Every view of a file counts as a download, so a share with a download limit shows no previews.
  const preview = !info.limited;
  files = (info.files ?? []).map((f) => ({
    ...f,
    type: 'file',
    path: f.path ?? '',
    src: preview && !f.noPreview && PREVIEWABLE.has(kindOf(f.name)) ? fileUrl(f) : undefined,
    text: !preview && kindOf(f.name) === 'text' ? 'This share has a download limit, so there is no preview. Download the file to see it.' : undefined,
  }));
  render();
}

function render() {
  let body;
  if (!info) body = html`<section class="card state"><p class="muted">Loading…</p></section>`;
  else if (info.status === 'gone' || info.status === 'revoked') body = stateCard('ban', 'This link doesn’t work', 'It may be mistyped, or the person who sent it turned it off. Ask them for a new link.');
  else if (info.status === 'expired') body = stateCard('clock', 'This share has expired', `It was available until ${date(info.expires)}. Ask the person who sent it for a new link.`);
  else if (info.status === 'full') body = stateCard('download', 'Download limit reached', 'These files can’t be downloaded anymore. Ask the person who sent the link to share them again.');
  else if (info.status === 'error') body = stateCard('alert', 'Couldn’t open this link', `${info.error} Try again in a moment.`);
  else if (info.locked) body = passwordGate(info.name, 'These files are password protected. Enter the password you were given.');
  else body = gallery();
  page.innerHTML = html`${brandHeader()}${body}`.s;
  page.querySelector('[autofocus]')?.focus();
}

function gallery() {
  const size = files.reduce((sum, f) => sum + f.size, 0);
  return html`
    <section class="share-head">
      <div class="min0">
        <h1>${info.name}</h1>
        <p class="muted">${files.length} ${files.length === 1 ? 'file' : 'files'} · ${bytes(size)} · ${info.expires ? `available until ${date(info.expires)}` : 'no expiry date'}</p>
      </div>
      ${files.length ? html`<a class="btn btn-primary" href="${base}/all.zip" download="${slug(info.name)}.zip">${icon('download')} Download all</a>` : ''}
    </section>
    ${info.message ? html`<blockquote class="message">${info.message}</blockquote>` : ''}
    ${files.length ? folderSections() : html`<div class="card empty">${icon('file')}<p>There are no files in this share anymore.</p></div>`}
    <details class="card cli">
      <summary>${icon('terminal')} Download from the command line</summary>
      <div class="cli-body"><p>Downloads everything as one zip file${files.some((f) => f.path) ? ', with its folders,' : ''} on macOS, Linux or Windows 10 and later.</p>${snippetTabs(shareSnippets(`${location.origin}${base}`, info))}</div>
    </details>`;
}

// A shared folder's files come grouped by subfolder, the files at the top first. Each group gets
// its own grid under the subfolder's name.
function folderSections() {
  const groups = [];
  files.forEach((f, i) => {
    if (groups.at(-1)?.path !== f.path) groups.push({ path: f.path, tiles: [] });
    groups.at(-1).tiles.push(fileTile(f, i));
  });
  if (groups.length === 1 && !groups[0].path) return html`<div class="grid">${groups[0].tiles}</div>`;
  return html`<div class="share-folders">${groups.map((g) => html`<section>
    ${g.path ? html`<h2 class="folder-head">${icon('folder')}<span class="min0">${folderText(g.path)}</span></h2>` : ''}
    <div class="grid">${g.tiles}</div>
  </section>`)}</div>`;
}

const fileTile = (f, i) => html`
  <div class="tile" role="button" tabindex="0" data-action="view" data-index="${i}" aria-label="View ${f.name}">
    <div class="thumb">${thumb(f)}</div>
    <div class="tile-body"><div class="tile-name" title="${f.name}">${f.name}</div><div class="tile-meta">${bytes(f.size)}</div></div>
    <a class="icon-btn tile-dl" href="${fileUrl(f, true)}" download data-action="download-file" aria-label="Download ${f.name}" title="Download">${icon('download')}</a>
  </div>`;

function view(index) {
  current = (index + files.length) % files.length;
  const f = files[current];
  const many = files.length > 1;
  lightbox.innerHTML = html`
    <div class="lb-top">
      <div class="min0"><div class="ellipsis strong">${f.name}</div><div class="small lb-muted ellipsis">${current + 1} of ${files.length} · ${bytes(f.size)}${f.path ? ` · ${folderText(f.path)}` : ''}</div></div>
      <a class="btn btn-sm" href="${fileUrl(f, true)}" download>${icon('download')} Download</a>
      <button type="button" class="icon-btn" data-action="close-lightbox" aria-label="Close">${icon('x')}</button>
    </div>
    <div class="lb-stage">
      ${many ? html`<button type="button" class="icon-btn lb-nav" data-action="step" data-step="-1" aria-label="Previous file">${icon('chevron-left')}</button>` : html`<span></span>`}
      <div class="lb-media">${previewMedia(f)}</div>
      ${many ? html`<button type="button" class="icon-btn lb-nav" data-action="step" data-step="1" aria-label="Next file">${icon('chevron-right')}</button>` : html`<span></span>`}
    </div>`.s;
  if (!lightbox.open) lightbox.showModal();
  if (kindOf(f.name) === 'text' && f.text === undefined) loadText(f);
}

async function loadText(f) {
  try {
    const res = await fetch(fileUrl(f), { headers: { Range: `bytes=0-${TEXT_PREVIEW - 1}` } });
    if (!res.ok) throw new Error(res.statusText);
    f.text = await res.text();
    if (f.size > TEXT_PREVIEW) f.text += '\n\n… The preview shows the first 256 KB. Download the file to see all of it.';
  } catch {
    f.text = 'Couldn’t load a preview of this file.';
  }
  const el = lightbox.querySelector('[data-text-preview]');
  if (el && files[current] === f) el.textContent = f.text;
}

delegate({
  view: (el) => view(Number(el.dataset.index)),
  step: (el) => view(current + Number(el.dataset.step)),
  'close-lightbox': () => lightbox.close(),
  'download-file': () => {}, // let the link download without also opening the viewer
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

document.addEventListener('keydown', (e) => {
  if (!lightbox.open) return;
  if (e.key === 'ArrowRight') view(current + 1);
  if (e.key === 'ArrowLeft') view(current - 1);
});

render();
loadInfo();

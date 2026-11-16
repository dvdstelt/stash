// The owner's files, upload links and share links, loaded from the server. The pages read it
// synchronously; every change goes to the server first and then everything is loaded again.

(() => {
const { request } = window.StashAPI;
const { kindOf } = window.StashUI;

const KB = 1e3, MB = 1e6, GB = 1e9, TB = 1e12;
const PREVIEWABLE = new Set(['image', 'pdf', 'video']);

let items = new Map();
let links = [];
let shares = [];
let storage = { used: 0, free: 0 };
let origin = location.origin;
let trashDays = 30;
let version = '';
let whatsNew = false;
let scanner = '';

const contentUrl = (id) => `/api/files/${encodeURIComponent(id)}/content`;
const uploadUrl = (r) => `${origin}/d/${r.token}`;
const shareUrl = (s) => `${origin}/s/${s.token}`;

function ingest(state) {
  const previous = items;
  items = new Map(state.items.map((it) => [it.id, it]));
  const linkFolders = new Set(state.links.map((l) => l.folder));
  const underLink = (it) => {
    for (let f = items.get(it.parent); f; f = items.get(f.parent)) if (linkFolders.has(f.id)) return true;
    return false;
  };
  for (const it of items.values()) {
    if (it.type !== 'file') continue;
    // A file with a virus, and a PDF that came through an upload link, also in a folder that came
    // with it, are sent as a download, so there is nothing to show inline.
    const downloadOnly = it.scan === 'virus' || (kindOf(it.name) === 'pdf' && underLink(it));
    if (PREVIEWABLE.has(kindOf(it.name)) && !downloadOnly) it.src = contentUrl(it.id);
    const before = previous.get(it.id);
    if (before?.text !== undefined) it.text = before.text; // keep text previews already fetched
  }
  links = state.links;
  shares = state.shares;
  storage = state.storage;
  origin = state.origin;
  trashDays = state.trashDays;
  version = state.version;
  whatsNew = state.whatsNew;
  scanner = state.scanner ?? '';
}

const byName = (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true });

const hasVirus = (it) => it.scan === 'virus';

/** The files in a folder and in the folders in there, leaving out what's in the trash. */
function filesBelow(folder) {
  const kids = new Map(); // one pass over everything, rather than one per folder
  for (const it of items.values()) {
    if (it.trashed) continue;
    if (!kids.has(it.parent)) kids.set(it.parent, []);
    kids.get(it.parent).push(it);
  }
  const out = [];
  const walk = (id) => (kids.get(id) ?? []).forEach((c) => (c.type === 'file' ? out.push(c) : walk(c.id)));
  walk(folder);
  return out;
}

const isExpired = (ts) => ts && ts <= Date.now();

const store = {
  async load() { ingest(await request('GET', '/api/state')); },

  item: (id) => items.get(id),

  /** What's in a folder, leaving out what's in the trash. */
  children(id) {
    const kids = [...items.values()].filter((i) => i.parent === id && !i.trashed);
    return [...kids.filter((i) => i.type === 'folder').sort(byName), ...kids.filter((i) => i.type === 'file').sort(byName)];
  },

  path(id) {
    const out = [];
    for (let it = items.get(id); it; it = items.get(it.parent)) out.unshift(it);
    return out;
  },

  /** Whether an item, or a folder it's in, was deleted. */
  inTrash(id) {
    for (let it = items.get(id); it; it = items.get(it.parent)) if (it.trashed) return true;
    return false;
  },

  files() { return [...items.values()].filter((i) => i.type === 'file' && !this.inTrash(i.id)); },

  search(query) {
    const q = query.toLowerCase();
    return [...items.values()].filter((i) => i.id !== 'root' && i.name.toLowerCase().includes(q) && !this.inTrash(i.id)).sort(byName);
  },

  size(id) {
    const it = items.get(id);
    if (!it) return 0;
    if (it.type === 'file') return it.size;
    return this.children(id).reduce((sum, c) => sum + this.size(c.id), 0);
  },

  /** Everything in a folder, the trash included: what deleting it for good removes. */
  contents(id) {
    const out = { files: 0, bytes: 0 };
    const walk = (it) => {
      if (it.type === 'file') {
        out.files++;
        out.bytes += it.size;
      } else {
        for (const c of items.values()) if (c.parent === it.id) walk(c);
      }
    };
    const it = items.get(id);
    if (it) walk(it);
    return out;
  },

  used: () => storage.used,
  free: () => storage.free,
  capacity: () => storage.used + storage.free,
  contentUrl,

  async createFolder(parent, name) {
    const it = await request('POST', '/api/folders', { parent, name });
    await this.load();
    return items.get(it.id);
  },

  async rename(id, name) {
    await request('PATCH', `/api/items/${encodeURIComponent(id)}`, { name });
    await this.load();
  },

  // The trash: deleted items, without what's inside a deleted folder, newest first.
  trash() { return [...items.values()].filter((i) => i.trashed && !this.inTrash(i.parent)).sort((a, b) => b.trashed - a.trashed); },
  trashDays: () => trashDays,
  purgeAt: (it) => it.trashed + trashDays * 864e5,

  async moveToTrash(ids) {
    await request('POST', '/api/items/trash', { ids });
    await this.load();
  },

  async restore(ids) {
    await request('POST', '/api/items/restore', { ids });
    await this.load();
  },

  async deleteForGood(ids) {
    await request('POST', '/api/items/delete', { ids });
    await this.load();
  },

  async emptyTrash() {
    await request('POST', '/api/trash/empty');
    await this.load();
  },

  // What's new
  version: () => version,
  /** Whether What's new has something the owner hasn't seen yet, so it opens by itself. */
  whatsNew: () => whatsNew,
  /** What each version brings, newest first. Versions the owner hasn't seen have `new`. */
  releases: () => request('GET', '/api/releases'),
  /** Got it: What's new doesn't open by itself again until a newer version runs. */
  async seenReleases() {
    await request('POST', '/api/releases/seen');
    await this.load();
  },

  /** "mark" or "refuse" when received files are checked for viruses, or "" when they aren't. */
  scanner: () => scanner,

  // Upload links
  requests: () => [...links].sort((a, b) => b.created - a.created),
  request: (id) => links.find((r) => r.id === id),
  requestForFolder: (folderId) => links.find((r) => r.folder === folderId),

  /** What came in through an upload link, counting folders that came with it, as the server does. */
  usage(r) {
    const files = filesBelow(r.folder);
    return { used: files.reduce((sum, f) => sum + f.size, 0), files: files.length, last: files.reduce((last, f) => Math.max(last, f.modified), 0) };
  },

  status(r) {
    if (r.revoked) return 'revoked';
    if (isExpired(r.expires)) return 'expired';
    const u = this.usage(r);
    if (u.used >= r.quota || (r.maxFiles && u.files >= r.maxFiles)) return 'full';
    return 'active';
  },

  async createRequest(data) {
    const r = await request('POST', '/api/links', data);
    await this.load();
    return this.request(r.id);
  },

  async updateRequest(id, patch) {
    await request('PATCH', `/api/links/${encodeURIComponent(id)}`, patch);
    await this.load();
  },

  // Share links
  shares: () => [...shares].sort((a, b) => b.created - a.created),
  share: (id) => shares.find((s) => s.id === id),
  /**
   * The files a share link offers: its files, or everything in its folder and the folders in
   * there. Files in the trash, and files with a virus, are left out, as they are for visitors.
   */
  shareFiles(s) {
    return this.sharedOrNot(s).filter((it) => !hasVirus(it));
  },
  sharedInTrash(s) { return s.files.filter((id) => items.has(id) && this.inTrash(id)).length; },
  /** Files a share link would offer, if the virus scanner hadn't found a virus in them. */
  sharedWithVirus(s) { return this.sharedOrNot(s).filter(hasVirus).length; },
  sharedOrNot(s) {
    if (!s.folder) return s.files.map((id) => items.get(id)).filter((it) => it && !this.inTrash(it.id));
    if (!items.has(s.folder) || this.inTrash(s.folder)) return [];
    return filesBelow(s.folder);
  },

  shareStatus(s) {
    if (s.revoked) return 'revoked';
    if (isExpired(s.expires)) return 'expired';
    if (s.maxDownloads && s.downloads >= s.maxDownloads) return 'full';
    return 'active';
  },

  async createShare(data) {
    const s = await request('POST', '/api/shares', data);
    await this.load();
    return this.share(s.id);
  },

  async updateShare(id, patch) {
    await request('PATCH', `/api/shares/${encodeURIComponent(id)}`, patch);
    await this.load();
  },
};

window.StashData = { KB, MB, GB, TB, uploadUrl, shareUrl, store };
})();

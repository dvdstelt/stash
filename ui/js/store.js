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

const contentUrl = (id) => `/api/files/${encodeURIComponent(id)}/content`;
const uploadUrl = (r) => `${origin}/d/${r.token}`;
const shareUrl = (s) => `${origin}/s/${s.token}`;

function ingest(state) {
  const previous = items;
  items = new Map(state.items.map((it) => {
    if (it.type === 'file') {
      if (PREVIEWABLE.has(kindOf(it.name))) it.src = contentUrl(it.id);
      const before = previous.get(it.id);
      if (before?.text !== undefined) it.text = before.text; // keep text previews already fetched
    }
    return [it.id, it];
  }));
  links = state.links;
  shares = state.shares;
  storage = state.storage;
  origin = state.origin;
}

const byName = (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true });
const isExpired = (ts) => ts && ts <= Date.now();

const store = {
  async load() { ingest(await request('GET', '/api/state')); },

  item: (id) => items.get(id),

  children(id) {
    const kids = [...items.values()].filter((i) => i.parent === id);
    return [...kids.filter((i) => i.type === 'folder').sort(byName), ...kids.filter((i) => i.type === 'file').sort(byName)];
  },

  path(id) {
    const out = [];
    for (let it = items.get(id); it; it = items.get(it.parent)) out.unshift(it);
    return out;
  },

  files: () => [...items.values()].filter((i) => i.type === 'file'),

  search(query) {
    const q = query.toLowerCase();
    return [...items.values()].filter((i) => i.id !== 'root' && i.name.toLowerCase().includes(q)).sort(byName);
  },

  size(id) {
    const it = items.get(id);
    if (!it) return 0;
    if (it.type === 'file') return it.size;
    return this.children(id).reduce((sum, c) => sum + this.size(c.id), 0);
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

  async remove(ids) {
    await request('POST', '/api/items/delete', { ids });
    await this.load();
  },

  // Upload links
  requests: () => [...links].sort((a, b) => b.created - a.created),
  request: (id) => links.find((r) => r.id === id),
  requestForFolder: (folderId) => links.find((r) => r.folder === folderId),

  usage(r) {
    const files = this.children(r.folder).filter((i) => i.type === 'file');
    return { used: files.reduce((sum, f) => sum + f.size, 0), files: files.length, last: Math.max(0, ...files.map((f) => f.modified)) };
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
  shareFiles: (s) => s.files.map((id) => items.get(id)).filter(Boolean),

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

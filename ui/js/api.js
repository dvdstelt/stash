// Talking to the Stash server: JSON requests, and uploads in resumable chunks.

(() => {
class ApiError extends Error {
  constructor(message, status, body) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

/** Sends a JSON request. The X-Stash header tells the server it comes from these pages. */
async function request(method, url, body) {
  const res = await fetch(url, {
    method,
    headers: { 'X-Stash': '1', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(data?.error ?? `The server answered ${res.status}.`, res.status, data);
  return data;
}

// 32 MB chunks stay under the 100 MB request limit of proxies such as Cloudflare's free plan,
// and a dropped connection costs at most one chunk.
const CHUNK = 32 * 1024 * 1024;
const RETRIES = 6;

function sendChunk(url, blob, onProgress, setAbort) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url);
    xhr.setRequestHeader('X-Stash', '1');
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.upload.onprogress = (e) => onProgress(e.loaded);
    xhr.onload = () => {
      let data = null;
      try { data = JSON.parse(xhr.responseText); } catch { /* not JSON */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(new ApiError(data?.error ?? `The server answered ${xhr.status}.`, xhr.status, data));
    };
    xhr.onerror = () => reject(new ApiError('The connection was lost.', 0));
    xhr.onabort = () => reject(new ApiError('Cancelled.', -1));
    setAbort(() => xhr.abort());
    xhr.send(blob);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Only a few files upload at once from one page; the rest wait their turn. The server also caps
// the uploads in progress, and one person dropping a folder shouldn't take every slot.
const PARALLEL = 4;
let running = 0;
const waiting = [];

function turn() {
  if (running < PARALLEL) {
    running++;
    return Promise.resolve();
  }
  return new Promise((resolve) => waiting.push(resolve));
}

function nextTurn() {
  const resolve = waiting.shift();
  if (resolve) resolve();
  else running--;
}

/**
 * Uploads a file in chunks. `base` is the uploads URL (/api/uploads, or /api/public/d/<token>/uploads)
 * and `fields` is what the server needs to start, such as the folder. onProgress gets the bytes
 * sent so far. Returns { done: Promise<item>, cancel() }.
 */
function uploadFile(file, { base, fields = {}, onProgress = () => {} }) {
  let abort = () => {};
  let cancelled = false;
  let id = null;

  const done = (async () => {
    await turn();
    try {
      if (cancelled) throw new ApiError('Cancelled.', -1);
      ({ id } = await request('POST', base, { ...fields, name: file.name, size: file.size }));
      let offset = 0;
      let failures = 0;
      for (;;) {
        if (cancelled) throw new ApiError('Cancelled.', -1);
        const end = Math.min(offset + CHUNK, file.size);
        try {
          const res = await sendChunk(`${base}/${id}?offset=${offset}`, file.slice(offset, end), (n) => onProgress(offset + n), (fn) => { abort = fn; });
          failures = 0;
          if (res.done) return res.item;
          offset = res.received;
        } catch (err) {
          if (cancelled || err.status === -1) throw new ApiError('Cancelled.', -1);
          if (err.status === 409 && typeof err.body?.received === 'number') { // the server has a different position: continue there
            offset = err.body.received;
            continue;
          }
          if (err.status !== 0 && err.status !== 400 && err.status < 500) throw err; // the server said no: don't retry
          if (++failures > RETRIES) throw new ApiError('The upload keeps failing. Check your connection and try again.', err.status);
          await sleep(Math.min(30000, 1000 * 2 ** (failures - 1)));
          try { // ask how much arrived before the connection broke
            offset = (await request('GET', `${base}/${id}`)).received;
          } catch (statusErr) {
            if (statusErr.status === 404) throw new ApiError('The server lost this upload. Start it again.', 404);
          }
        }
        onProgress(offset);
      }
    } finally {
      nextTurn();
    }
  })();

  return {
    done,
    cancel() {
      cancelled = true;
      abort();
      if (id) request('DELETE', `${base}/${id}`).catch(() => {});
    },
  };
}

window.StashAPI = { ApiError, request, uploadFile };
})();

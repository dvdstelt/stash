// Sign-in page. The first time, it's where you choose your password, using the setup link
// Stash prints when it starts without one.

const { html, icon, delegate } = window.StashUI;
const { request } = window.StashAPI;

const page = document.getElementById('page');
const setupToken = new URLSearchParams(location.search).get('setup');

function brand() {
  return html`<header class="public-brand login-brand"><span class="brand-mark">${icon('briefcase')}</span><strong>Stash</strong></header>`;
}

function form({ title, text, fields, button, submit }) {
  return html`${brand()}<section class="card login-card">
    <h1>${title}</h1>
    <p class="muted">${text}</p>
    <form data-submit="${submit}" class="login-form">
      ${fields}
      <p class="form-error" role="alert" data-error hidden></p>
      <button class="btn btn-primary" type="submit">${button}</button>
    </form>
  </section>`;
}

async function render() {
  let session;
  try {
    session = await request('GET', '/api/session');
  } catch (err) {
    page.innerHTML = html`${brand()}<section class="card login-card"><h1>Can’t reach Stash</h1><p class="muted">${err.message}</p></section>`.s;
    return;
  }
  if (session.signedIn) {
    location.replace('/');
    return;
  }
  if (session.setupRequired && !setupToken) {
    page.innerHTML = html`${brand()}<section class="card login-card">
      <h1>Choose a password first</h1>
      <p class="muted">Stash doesn’t have a password yet. Open the setup link it printed when it started; you’ll find it in its log, for example with <code>podman logs stash</code>.</p>
    </section>`.s;
    return;
  }
  page.innerHTML = (session.setupRequired
    ? form({
      title: 'Choose your password',
      text: 'You’ll use it to sign in to your files. Everyone else only uses the links you send them.',
      fields: html`
        <div class="field"><label for="pw">Password</label><input id="pw" name="password" class="input" type="password" autocomplete="new-password" minlength="10" required autofocus><p class="hint">At least 10 characters.</p></div>
        <div class="field"><label for="pw2">Type it again</label><input id="pw2" name="again" class="input" type="password" autocomplete="new-password" required></div>`,
      button: 'Save password',
      submit: 'setup',
    })
    : form({
      title: 'Sign in',
      text: 'Enter your password to get to your files.',
      fields: html`<div class="field"><label for="pw">Password</label><input id="pw" name="password" class="input" type="password" autocomplete="current-password" required autofocus></div>`,
      button: 'Sign in',
      submit: 'login',
    })).s;
  page.querySelector('[autofocus]')?.focus();
}

async function submit(form, send) {
  const box = form.querySelector('[data-error]');
  const button = form.querySelector('button');
  button.disabled = true;
  try {
    await send();
    location.replace('/');
  } catch (err) {
    box.textContent = err.message;
    box.hidden = false;
    button.disabled = false;
  }
}

delegate({}, {
  login: (form) => submit(form, () => request('POST', '/api/login', { password: form.elements.password.value })),
  setup: (form) => {
    const { password, again } = form.elements;
    if (password.value !== again.value) {
      again.setCustomValidity('The passwords don’t match.');
      again.reportValidity();
      again.addEventListener('input', () => again.setCustomValidity(''), { once: true });
      return;
    }
    submit(form, () => request('POST', '/api/setup', { token: setupToken, password: password.value }));
  },
});

render();

// Apply a remembered light or dark choice before the page draws, so it doesn't flash.
try {
  const theme = new URLSearchParams(location.search).get('theme') || localStorage.getItem('theme');
  if (theme === 'dark' || theme === 'light') document.documentElement.dataset.theme = theme;
} catch { /* storage unavailable */ }

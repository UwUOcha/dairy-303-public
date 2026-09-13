// Public installation configuration is embedded in both the application and login page.
export const installation = (() => {
  try { return JSON.parse(globalThis.document?.querySelector('meta[name="app-config"]')?.content || '{}') || {}; }
  catch { return {}; }
})();

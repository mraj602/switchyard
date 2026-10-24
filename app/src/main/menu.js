// The app menu (C9b): Watchyard menu (Hub, Account 1…9, Refresh usage, Run at login,
// Keep running, Quit), editMenu, toggleDevTools only when unpackaged.

/**
 * @param {{electron: object, window: object, tabs: object, usage: object, autostart: object,
 *          store: object, isPackaged: boolean, bus?: object, platform?: string,
 *          setSettings?: (patch: object) => Promise<object>, log?: object}} deps
 *   setSettings: background.setSettings, late-bound by index.js (background is made after the
 *   menu); without it the two checkboxes are disabled. bus: rebuilds on `tabs` and `settings`.
 * @returns {{build(): Promise<void>, template(): object[]}}   // Menu.setApplicationMenu; rebuilt on tabs/settings bus events
 */
export function create(deps) {
  const { electron, window, tabs, usage, autostart, store, isPackaged, bus } = deps;
  const log = deps.log ?? console;
  const platform = deps.platform ?? process.platform;
  let loginItem = false; // the last value read from the OS
  let subscribed = false;

  const select = (id) => {
    window.select(id);
    window.showWindow();
  };

  function patch(p) {
    const fn = deps.setSettings;
    if (!fn) return;
    Promise.resolve(fn(p)).catch((err) => log.error('menu: settings failed:', err));
  }

  function template() {
    const s = store.get().settings ?? {};
    const list = (tabs.list?.() ?? []).slice(0, 9);
    const canSet = typeof deps.setSettings === 'function';
    const watchyard = [
      { label: 'Hub', accelerator: 'CmdOrCtrl+0', click: () => select('hub') },
      ...list.map((t, i) => ({ label: `Account ${i + 1}: ${t.name ?? t.id}`, accelerator: `CmdOrCtrl+${i + 1}`, click: () => select(t.id) })),
      { type: 'separator' },
      { label: 'Refresh usage', accelerator: 'CmdOrCtrl+R', click: () => Promise.resolve(usage.refresh()).catch((err) => log.error('menu: refresh failed:', err)) },
      { type: 'separator' },
      { label: 'Run at login', type: 'checkbox', checked: loginItem, enabled: canSet, click: (item) => patch({ autostart: Boolean(item.checked) }) },
      { label: 'Keep running', type: 'checkbox', checked: s.keep_running !== false, enabled: canSet, click: (item) => patch({ keep_running: Boolean(item.checked) }) },
      { type: 'separator' },
      { label: 'Quit', accelerator: 'CmdOrCtrl+Q', click: () => electron.app.quit() },
    ];
    const out = [{ label: platform === 'darwin' ? electron.app.name : 'Watchyard', submenu: watchyard }, { role: 'editMenu' }];
    if (!isPackaged) out.push({ label: 'View', submenu: [{ role: 'toggleDevTools' }] });
    return out;
  }

  function apply() {
    electron.Menu.setApplicationMenu(electron.Menu.buildFromTemplate(template()));
  }

  const api = {
    template,
    async build() {
      if (!subscribed && bus) {
        subscribed = true;
        const rebuild = () => {
          api.build().catch((err) => log.error('menu: rebuild failed:', err));
        };
        bus.on('tabs', rebuild);
        bus.on('settings', rebuild);
      }
      apply();
      try {
        const now = await autostart.get();
        if (now !== loginItem) {
          loginItem = now;
          apply();
        }
      } catch (err) {
        log.warn?.('menu: autostart read failed:', err?.message ?? err);
      }
    },
  };
  return api;
}

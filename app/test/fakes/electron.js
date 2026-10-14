// A fake of the Electron main-process API, for unit tests on plain Node (no display, no
// Electron binary). Every app module takes Electron as an argument (`create({electron, …})`),
// so a test passes `fakeElectron().electron`. Every call is recorded in `calls` as
// {api: 'shell.openExternal', args: [...]}; objects are EventEmitters, so a test can
// `emit('did-finish-load')` on a fake webContents or `emit('ready')` on app.
//
// Wave 0 owns this file. A work package that needs more of Electron asks wave 0's owner (or
// wraps the fake in its own test helper); keep it a recorder, not a simulator.
//
//   const fake = fakeElectron({ platform: 'linux', appData: '/tmp/x' });
//   const { app, session, ipcMain } = fake.electron;
//   fake.callsOf('shell.openExternal')  // → [[url], …]

import { EventEmitter } from 'node:events';
import { createHash } from 'node:crypto';

/**
 * @param {object} [opts]
 * @param {string} [opts.platform]           process.platform to report (app.dock exists on darwin)
 * @param {string} [opts.appData]            app.getPath('appData')
 * @param {Record<string,string>} [opts.paths] other app.getPath values
 * @param {boolean} [opts.isPackaged]
 * @param {string} [opts.version]            app.getVersion()
 * @param {boolean} [opts.singleInstance]    requestSingleInstanceLock() result (default true)
 * @param {boolean} [opts.encryption]        safeStorage.isEncryptionAvailable() (default true)
 * @param {string} [opts.storageBackend]     safeStorage.getSelectedStorageBackend() (linux; default 'gnome_libsecret')
 * @param {string} [opts.safeStorageKey]     the fake encryption key; another key cannot decrypt
 * @param {(wc: FakeWebContents, code: string, world?: number) => any} [opts.executeJavaScript]
 *                                          result of executeJavaScript / executeJavaScriptInIsolatedWorld
 * @param {(partition: string, url: string, init?: object) => Promise<Response>} [opts.sessionFetch]
 * @param {number} [opts.dialogResponse]     dialog.showMessageBox response index (default 0)
 */
export function fakeElectron(opts = {}) {
  const platform = opts.platform ?? 'linux';
  const calls = [];
  const record = (api, args) => {
    calls.push({ api, args: [...args] });
  };
  const callsOf = (api) => calls.filter((c) => c.api === api).map((c) => c.args);

  let nextWebContentsId = 1;
  const allWebContents = new Map();

  // ---- session

  class FakeCookies extends EventEmitter {
    constructor(partition) {
      super();
      this.partition = partition;
      /** @type {Map<string, object>} key: name|domain|path */
      this.jar = new Map();
    }
    async get(filter = {}) {
      record('cookies.get', [this.partition, filter]);
      return [...this.jar.values()].filter((c) =>
        Object.entries(filter).every(([k, v]) => (k === 'url' ? cookieMatchesUrl(c, v) : c[k] === v)),
      );
    }
    async set(details) {
      record('cookies.set', [this.partition, details]);
      const domain = details.domain ?? (details.url ? new URL(details.url).hostname : '');
      const c = { name: details.name, value: details.value ?? '', domain, path: details.path ?? '/', ...details };
      delete c.url;
      this.jar.set(`${c.name}|${c.domain}|${c.path}`, c);
    }
    async remove(url, name) {
      record('cookies.remove', [this.partition, url, name]);
      const host = new URL(url).hostname;
      for (const [k, c] of this.jar) if (c.name === name && c.domain.replace(/^\./, '') === host) this.jar.delete(k);
    }
    async flushStore() {
      record('cookies.flushStore', [this.partition]);
    }
  }

  class FakeSession extends EventEmitter {
    constructor(partition) {
      super();
      this.partition = partition;
      this.cookies = new FakeCookies(partition);
      this.handlers = {};
      this.userAgent = 'Mozilla/5.0 (fake) Electron/44.0.0';
      this.protocol = fakeProtocol(`session(${partition}).protocol`);
    }
    setPermissionRequestHandler(fn) {
      record('session.setPermissionRequestHandler', [this.partition]);
      this.handlers.permissionRequest = fn;
    }
    setPermissionCheckHandler(fn) {
      record('session.setPermissionCheckHandler', [this.partition]);
      this.handlers.permissionCheck = fn;
    }
    setDevicePermissionHandler(fn) {
      record('session.setDevicePermissionHandler', [this.partition]);
      this.handlers.devicePermission = fn;
    }
    setDisplayMediaRequestHandler(fn) {
      record('session.setDisplayMediaRequestHandler', [this.partition]);
      this.handlers.displayMedia = fn;
    }
    setCertificateVerifyProc(fn) {
      record('session.setCertificateVerifyProc', [this.partition]);
      this.handlers.certificateVerify = fn;
    }
    /** Test helper: what the permission request handler answers (true/false). */
    requestPermission(permission, details = {}, webContents = null) {
      const fn = this.handlers.permissionRequest;
      if (!fn) return Promise.resolve(false);
      return new Promise((resolve) => fn(webContents, permission, resolve, details));
    }
    /** Test helper: what the permission check handler answers. */
    checkPermission(permission, origin = '', details = {}, webContents = null) {
      const fn = this.handlers.permissionCheck;
      return fn ? fn(webContents, permission, origin, details) : false;
    }
    setUserAgent(ua) {
      record('session.setUserAgent', [this.partition, ua]);
      this.userAgent = ua;
    }
    getUserAgent() {
      return this.userAgent;
    }
    async clearStorageData(options) {
      record('session.clearStorageData', [this.partition, options]);
      this.cookies.jar.clear();
    }
    async clearCache() {
      record('session.clearCache', [this.partition]);
    }
    async fetch(url, init) {
      record('session.fetch', [this.partition, String(url), init]);
      if (opts.sessionFetch) return opts.sessionFetch(this.partition, String(url), init);
      throw new Error('fake session.fetch: no opts.sessionFetch');
    }
    getStoragePath() {
      return this.partition.startsWith('persist:') ? `${appPaths.userData}/Partitions/${this.partition.slice(8)}` : null;
    }
  }

  const sessions = new Map();
  const defaultSession = new FakeSession('');
  const session = {
    defaultSession,
    fromPartition(partition, options) {
      record('session.fromPartition', [partition, options]);
      if (!sessions.has(partition)) sessions.set(partition, new FakeSession(partition));
      return sessions.get(partition);
    },
    /** Test helper: every session made so far, by partition. */
    _all: sessions,
  };

  // ---- webContents

  class FakeWebContents extends EventEmitter {
    constructor(webPreferences = {}) {
      super();
      this.id = nextWebContentsId++;
      this.webPreferences = { ...webPreferences };
      this.session = webPreferences.partition ? session.fromPartition(webPreferences.partition) : webPreferences.session ?? defaultSession;
      this.url = '';
      this.destroyed = false;
      this.windowOpenHandler = null;
      this.userAgent = '';
      this.audioMuted = false;
      this.mainFrame = { url: '', routingId: 1, processId: this.id, frames: [] };
      this.sent = [];
      allWebContents.set(this.id, this);
    }
    async loadURL(url, options) {
      record('webContents.loadURL', [this.id, url, options]);
      this.url = url;
      this.mainFrame.url = url;
    }
    getURL() {
      return this.url;
    }
    /** Test helper: as if the page navigated (no events). */
    _setURL(url) {
      this.url = url;
      this.mainFrame.url = url;
    }
    reload() {
      record('webContents.reload', [this.id]);
    }
    stop() {
      record('webContents.stop', [this.id]);
    }
    setWindowOpenHandler(fn) {
      record('webContents.setWindowOpenHandler', [this.id]);
      this.windowOpenHandler = fn;
    }
    /** Test helper: what the window open handler answers for a URL. */
    _windowOpen(url, details = {}) {
      return this.windowOpenHandler ? this.windowOpenHandler({ url, frameName: '', features: '', disposition: 'new-window', ...details }) : { action: 'allow' };
    }
    /**
     * Test helper: emit a cancellable navigation event ('will-navigate', 'will-redirect',
     * 'will-attach-webview', …) and say whether a listener called preventDefault.
     */
    _cancellable(name, ...rest) {
      let prevented = false;
      const event = { preventDefault: () => (prevented = true), url: rest[0] };
      this.emit(name, event, ...rest);
      return prevented;
    }
    async executeJavaScript(code, userGesture) {
      record('webContents.executeJavaScript', [this.id, code, userGesture]);
      return opts.executeJavaScript ? opts.executeJavaScript(this, code) : undefined;
    }
    async executeJavaScriptInIsolatedWorld(worldId, scripts, userGesture) {
      record('webContents.executeJavaScriptInIsolatedWorld', [this.id, worldId, scripts, userGesture]);
      return opts.executeJavaScript ? opts.executeJavaScript(this, scripts.map((s) => s.code).join('\n'), worldId) : undefined;
    }
    send(channel, ...args) {
      record('webContents.send', [this.id, channel, ...args]);
      this.sent.push({ channel, args });
    }
    setUserAgent(ua) {
      record('webContents.setUserAgent', [this.id, ua]);
      this.userAgent = ua;
    }
    getUserAgent() {
      return this.userAgent || this.session.getUserAgent();
    }
    setAudioMuted(m) {
      this.audioMuted = m;
    }
    focus() {
      record('webContents.focus', [this.id]);
    }
    openDevTools(o) {
      record('webContents.openDevTools', [this.id, o]);
    }
    isDestroyed() {
      return this.destroyed;
    }
    close() {
      record('webContents.close', [this.id]);
      this.destroyed = true;
      allWebContents.delete(this.id);
      this.emit('destroyed');
    }
    isLoading() {
      return false;
    }
  }

  const webContents = {
    getAllWebContents: () => [...allWebContents.values()],
    fromId: (id) => allWebContents.get(id),
    getFocusedWebContents: () => null,
  };

  // ---- views and windows

  class FakeView extends EventEmitter {
    constructor(options = {}) {
      super();
      record('WebContentsView.new', [options]);
      this.options = options;
      this.webContents = new FakeWebContents(options.webPreferences ?? {});
      this.bounds = { x: 0, y: 0, width: 0, height: 0 };
      this.visible = true;
      this.children = [];
    }
    setBounds(b) {
      this.bounds = { ...b };
    }
    getBounds() {
      return { ...this.bounds };
    }
    setVisible(v) {
      this.visible = v;
    }
    getVisible() {
      return this.visible;
    }
    setBackgroundColor(c) {
      this.backgroundColor = c;
    }
    addChildView(v, index) {
      this.removeChildView(v);
      if (index === undefined) this.children.push(v);
      else this.children.splice(index, 0, v);
    }
    removeChildView(v) {
      this.children = this.children.filter((c) => c !== v);
    }
  }

  const windows = [];
  class FakeBrowserWindow extends EventEmitter {
    constructor(options = {}) {
      super();
      record('BrowserWindow.new', [options]);
      this.options = options;
      this.webContents = new FakeWebContents(options.webPreferences ?? {});
      this.contentView = new FakeView();
      this.contentView.webContents = this.webContents;
      this.visible = options.show !== false;
      this.focused = false;
      this.minimized = false;
      this.destroyed = false;
      this.bounds = { x: 0, y: 0, width: options.width ?? 800, height: options.height ?? 600 };
      windows.push(this);
    }
    static getAllWindows() {
      return windows.filter((w) => !w.destroyed);
    }
    async loadURL(url, o) {
      return this.webContents.loadURL(url, o);
    }
    show() {
      record('BrowserWindow.show', []);
      this.visible = true;
      this.emit('show');
    }
    hide() {
      record('BrowserWindow.hide', []);
      this.visible = false;
      this.emit('hide');
    }
    focus() {
      record('BrowserWindow.focus', []);
      this.focused = true;
      this.emit('focus');
    }
    restore() {
      this.minimized = false;
    }
    isVisible() {
      return this.visible;
    }
    isFocused() {
      return this.focused;
    }
    isMinimized() {
      return this.minimized;
    }
    isDestroyed() {
      return this.destroyed;
    }
    getContentBounds() {
      return { ...this.bounds };
    }
    getBounds() {
      return { ...this.bounds };
    }
    setBounds(b) {
      this.bounds = { ...this.bounds, ...b };
      this.emit('resize');
    }
    setMinimumSize(w, h) {
      this.minimumSize = [w, h];
    }
    setOverlayIcon(image, description) {
      record('BrowserWindow.setOverlayIcon', [image, description]);
    }
    setMenuBarVisibility(v) {
      this.menuBarVisible = v;
    }
    setTitle(t) {
      this.title = t;
    }
    close() {
      record('BrowserWindow.close', []);
      let prevented = false;
      this.emit('close', { preventDefault: () => (prevented = true) });
      if (!prevented) this.destroy();
    }
    destroy() {
      this.destroyed = true;
      this.emit('closed');
    }
  }

  // ---- app

  const appPaths = {
    appData: opts.appData ?? '/fake/appData',
    home: '/fake/home',
    temp: '/fake/tmp',
    exe: '/fake/app/switchyard-desktop',
    logs: '/fake/logs',
    ...(opts.paths ?? {}),
  };
  appPaths.userData ??= `${appPaths.appData}/Switchyard`; // Electron's default from productName

  class FakeApp extends EventEmitter {
    constructor() {
      super();
      this.isPackaged = opts.isPackaged ?? false;
      this.name = 'Switchyard';
      this.readyFired = false;
      this.loginItem = { openAtLogin: false, openAsHidden: false, args: [] };
      this.badgeCount = 0;
      this.commandLine = {
        appendSwitch: (...a) => record('app.commandLine.appendSwitch', a),
        hasSwitch: () => false,
        getSwitchValue: () => '',
      };
      if (platform === 'darwin') {
        this.dock = {
          setBadge: (t) => record('app.dock.setBadge', [t]),
          getBadge: () => '',
          hide: () => record('app.dock.hide', []),
          show: () => record('app.dock.show', []),
        };
      }
    }
    getPath(name) {
      if (!(name in appPaths)) throw new Error(`fake app.getPath: unknown ${name}`);
      return appPaths[name];
    }
    setPath(name, value) {
      record('app.setPath', [name, value]);
      if (this.readyFired && name === 'userData') throw new Error('fake: setPath(userData) after ready');
      appPaths[name] = value;
    }
    getVersion() {
      return opts.version ?? '0.6.0';
    }
    getName() {
      return this.name;
    }
    getAppPath() {
      return '/fake/app';
    }
    isReady() {
      return this.readyFired;
    }
    whenReady() {
      return this.readyFired ? Promise.resolve() : new Promise((r) => this.once('ready', () => r()));
    }
    /** Test helper: fire 'ready'. */
    _ready() {
      this.readyFired = true;
      this.emit('ready', {}, {});
    }
    enableSandbox() {
      record('app.enableSandbox', []);
      this.sandboxed = true;
    }
    requestSingleInstanceLock(data) {
      record('app.requestSingleInstanceLock', [data]);
      return opts.singleInstance ?? true;
    }
    releaseSingleInstanceLock() {
      record('app.releaseSingleInstanceLock', []);
    }
    setLoginItemSettings(s) {
      record('app.setLoginItemSettings', [s]);
      this.loginItem = { ...this.loginItem, ...s };
    }
    getLoginItemSettings() {
      return { ...this.loginItem };
    }
    setBadgeCount(n) {
      record('app.setBadgeCount', [n]);
      this.badgeCount = n;
      return true;
    }
    getBadgeCount() {
      return this.badgeCount;
    }
    setAppUserModelId(id) {
      record('app.setAppUserModelId', [id]);
    }
    disableHardwareAcceleration() {
      record('app.disableHardwareAcceleration', []);
    }
    focus(o) {
      record('app.focus', [o]);
    }
    quit() {
      record('app.quit', []);
      let prevented = false;
      this.emit('before-quit', { preventDefault: () => (prevented = true) });
      if (!prevented) this.emit('will-quit', { preventDefault() {} });
    }
    exit(code = 0) {
      record('app.exit', [code]);
    }
    relaunch(o) {
      record('app.relaunch', [o]);
    }
  }
  const app = new FakeApp();

  // ---- ipc

  class FakeIpcMain extends EventEmitter {
    constructor() {
      super();
      this.handlers = new Map();
    }
    handle(channel, fn) {
      record('ipcMain.handle', [channel]);
      if (this.handlers.has(channel)) throw new Error(`Attempted to register a second handler for '${channel}'`);
      this.handlers.set(channel, fn);
    }
    handleOnce(channel, fn) {
      record('ipcMain.handleOnce', [channel]);
      this.handle(channel, fn);
    }
    removeHandler(channel) {
      record('ipcMain.removeHandler', [channel]);
      this.handlers.delete(channel);
    }
    on(channel, fn) {
      record('ipcMain.on', [channel]);
      return super.on(channel, fn);
    }
    /**
     * Test helper: call the handler of channel as Electron would, with a fake event from
     * `sender` (a FakeWebContents; its mainFrame is the senderFrame unless `frame` is given).
     */
    async _invoke(channel, sender, args, frame) {
      const fn = this.handlers.get(channel);
      if (!fn) throw new Error(`No handler registered for '${channel}'`);
      const event = { sender, senderFrame: frame ?? sender?.mainFrame ?? null, processId: sender?.id ?? 0, frameId: 1 };
      return args === undefined ? fn(event) : fn(event, args);
    }
  }
  const ipcMain = new FakeIpcMain();

  // ---- notifications, tray, menu, images

  const notifications = [];
  class FakeNotification extends EventEmitter {
    constructor(options = {}) {
      super();
      record('Notification.new', [options]);
      this.options = options;
      this.shown = false;
      notifications.push(this);
    }
    static isSupported() {
      return true;
    }
    show() {
      record('Notification.show', [this.options.title]);
      this.shown = true;
      this.emit('show');
    }
    close() {
      record('Notification.close', [this.options.title]);
      this.emit('close');
    }
    /** Test helper: as if the person clicked it. */
    _click() {
      this.emit('click', {});
    }
  }

  const trays = [];
  class FakeTray extends EventEmitter {
    constructor(image, guid) {
      super();
      record('Tray.new', [image?.path ?? image, guid]);
      this.image = image;
      this.destroyed = false;
      trays.push(this);
    }
    setImage(image) {
      record('Tray.setImage', [image?.path ?? image]);
      this.image = image;
    }
    setToolTip(t) {
      this.toolTip = t;
    }
    setTitle(t) {
      record('Tray.setTitle', [t]);
      this.title = t;
    }
    setContextMenu(menu) {
      record('Tray.setContextMenu', [menu]);
      this.menu = menu;
    }
    destroy() {
      this.destroyed = true;
    }
    isDestroyed() {
      return this.destroyed;
    }
  }

  let applicationMenu = null;
  const Menu = {
    buildFromTemplate(template) {
      record('Menu.buildFromTemplate', [template]);
      return { template, items: template.map((t) => ({ ...t })), popup() {} };
    },
    setApplicationMenu(m) {
      record('Menu.setApplicationMenu', [m]);
      applicationMenu = m;
    },
    getApplicationMenu: () => applicationMenu,
  };

  const image = (path, extra = {}) => ({
    path,
    isEmpty: () => !path && !extra.buffer,
    getSize: () => ({ width: 16, height: 16 }),
    toPNG: () => extra.buffer ?? Buffer.alloc(0),
    resize: () => image(path, extra),
    setTemplateImage(v) {
      this.template = v;
    },
    ...extra,
  });
  const nativeImage = {
    createFromPath: (p) => (record('nativeImage.createFromPath', [p]), image(p)),
    createFromBuffer: (b) => image(null, { buffer: b }),
    createFromDataURL: (u) => image(null, { dataURL: u }),
    createEmpty: () => image(null),
  };

  // ---- safeStorage: reversible with the right key, unreadable with another

  const key = createHash('sha256').update(opts.safeStorageKey ?? 'fake-safe-storage-key').digest();
  const keyId = key.subarray(0, 4).toString('hex');
  const MAGIC = `fakeenc:${keyId}:`;
  const xor = (buf) => Buffer.from(buf.map((b, i) => b ^ key[i % key.length]));
  const safeStorage = {
    isEncryptionAvailable: () => opts.encryption ?? true,
    getSelectedStorageBackend: () => (platform === 'linux' ? opts.storageBackend ?? 'gnome_libsecret' : 'unknown'),
    encryptString(text) {
      record('safeStorage.encryptString', []); // never record the secret
      if (!(opts.encryption ?? true)) throw new Error('Encryption is not available.');
      return Buffer.concat([Buffer.from(MAGIC), xor(Buffer.from(String(text), 'utf8'))]);
    },
    decryptString(buf) {
      record('safeStorage.decryptString', []);
      const b = Buffer.from(buf);
      if (!b.subarray(0, MAGIC.length).equals(Buffer.from(MAGIC))) {
        throw new Error('Error while decrypting the ciphertext provided to safeStorage.decryptString.');
      }
      return xor(b.subarray(MAGIC.length)).toString('utf8');
    },
  };

  // ---- protocol

  function fakeProtocol(name) {
    const handlers = new Map();
    return {
      registerSchemesAsPrivileged(schemes) {
        record(`${name}.registerSchemesAsPrivileged`, [schemes]);
        this.privileged = schemes;
      },
      handle(scheme, fn) {
        record(`${name}.handle`, [scheme]);
        if (handlers.has(scheme)) throw new Error(`fake: ${scheme} already handled`);
        handlers.set(scheme, fn);
      },
      unhandle(scheme) {
        record(`${name}.unhandle`, [scheme]);
        handlers.delete(scheme);
      },
      isProtocolHandled: (scheme) => handlers.has(scheme),
      /** Test helper: run the handler of url's scheme with a real Request; returns its Response. */
      async _request(url, init) {
        const scheme = new URL(url).protocol.slice(0, -1);
        const fn = handlers.get(scheme);
        if (!fn) throw new Error(`fake: no handler for ${scheme}`);
        return fn(new Request(url, init));
      },
    };
  }
  const protocol = fakeProtocol('protocol');

  // ---- shell, dialog, renderer side (for the preload test)

  const shell = {
    openExternal: async (url, o) => {
      record('shell.openExternal', [url, o]);
    },
    openPath: async (p) => {
      record('shell.openPath', [p]);
      return '';
    },
    showItemInFolder: (p) => record('shell.showItemInFolder', [p]),
  };

  const dialog = {
    showMessageBox: async (...a) => {
      record('dialog.showMessageBox', a);
      return { response: opts.dialogResponse ?? 0, checkboxChecked: false };
    },
    showMessageBoxSync: (...a) => {
      record('dialog.showMessageBoxSync', a);
      return opts.dialogResponse ?? 0;
    },
    showErrorBox: (...a) => record('dialog.showErrorBox', a),
  };

  class FakeIpcRenderer extends EventEmitter {
    constructor() {
      super();
      /** Test hook: (channel, ...args) → reply of invoke. */
      this.reply = async () => undefined;
    }
    invoke(channel, ...args) {
      record('ipcRenderer.invoke', [channel, ...args]);
      return Promise.resolve(this.reply(channel, ...args));
    }
    send(channel, ...args) {
      record('ipcRenderer.send', [channel, ...args]);
    }
    sendSync(channel, ...args) {
      record('ipcRenderer.sendSync', [channel, ...args]);
    }
    /** Test helper: as if main sent channel with payload (the listener gets a fake event first). */
    _fromMain(channel, ...args) {
      this.emit(channel, { sender: this, ports: [] }, ...args);
    }
  }
  const ipcRenderer = new FakeIpcRenderer();
  const exposed = {};
  const contextBridge = {
    exposeInMainWorld(key, api) {
      record('contextBridge.exposeInMainWorld', [key]);
      exposed[key] = api;
    },
  };

  const electron = {
    app,
    session,
    webContents,
    BrowserWindow: FakeBrowserWindow,
    WebContentsView: FakeView,
    ipcMain,
    Notification: FakeNotification,
    Tray: FakeTray,
    Menu,
    nativeImage,
    safeStorage,
    protocol,
    shell,
    dialog,
    // renderer side
    ipcRenderer,
    contextBridge,
  };

  return {
    electron,
    platform,
    calls,
    record,
    callsOf,
    /** Recorded instances, for assertions. */
    windows,
    notifications,
    trays,
    exposed,
    FakeWebContents,
  };
}

function cookieMatchesUrl(c, url) {
  const host = new URL(url).hostname;
  const d = c.domain.replace(/^\./, '');
  return host === d || host.endsWith(`.${d}`);
}

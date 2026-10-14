const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('sy', {
  state: () => ipcRenderer.invoke('state'),
  select: (id) => ipcRenderer.invoke('select', id),
  add: (acct) => ipcRenderer.invoke('add', acct),
  update: (id, patch) => ipcRenderer.invoke('update', id, patch),
  remove: (id) => ipcRenderer.invoke('remove', id),
  reorder: (ids) => ipcRenderer.invoke('reorder', ids),
  refresh: (id) => ipcRenderer.invoke('refresh', id),
  code: (id, url) => ipcRenderer.invoke('code', id, url),
  reload: (id) => ipcRenderer.invoke('reload', id),
  home: (id) => ipcRenderer.invoke('home', id),
  external: (id) => ipcRenderer.invoke('external', id),
  analytics: (q) => ipcRenderer.invoke('analytics', q),
  settings: () => ipcRenderer.invoke('settings'),
  setSettings: (patch) => ipcRenderer.invoke('settings:set', patch),
  syncNow: () => ipcRenderer.invoke('sync-now'),
  onState: (cb) => ipcRenderer.on('state', (_e, s) => cb(s)),
});

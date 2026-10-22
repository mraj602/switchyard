import { KEYCHAIN_ACCESSIBLE, SECURE_HUB_KEY, SECURE_REVOKE_KEY } from '../../contract/constants';
import type { SecureStoreLike, SecureStoreOptions } from '../../contract/platform';
import { createMemorySecureStore, createSecureHub, secureOptions } from './index';
import type { StoredHub } from './index';

const hub: StoredHub = {
  origin: 'https://hub.test.ts.net',
  token: 'sy_p_TOKEN',
  device: 'dev_phone',
  name: 'Pixel · app',
  paired_at: '2026-10-13T12:00:00.000Z',
};

function recording(inner: SecureStoreLike) {
  const calls: { op: string; key: string; options?: SecureStoreOptions }[] = [];
  const s: SecureStoreLike = {
    async getItemAsync(k, o) {
      calls.push({ op: 'get', key: k, options: o });
      return inner.getItemAsync(k, o);
    },
    async setItemAsync(k, v, o) {
      calls.push({ op: 'set', key: k, options: o });
      return inner.setItemAsync(k, v, o);
    },
    async deleteItemAsync(k, o) {
      calls.push({ op: 'delete', key: k, options: o });
      return inner.deleteItemAsync(k, o);
    },
  };
  return { s, calls };
}

describe('SecureHub', () => {
  it('round-trips the entry under sy.hub.v1', async () => {
    const mem = createMemorySecureStore();
    const sh = createSecureHub(mem, secureOptions(KEYCHAIN_ACCESSIBLE));
    expect(await sh.load()).toBeNull();
    await sh.save(hub);
    expect(Object.keys(mem.dump())).toEqual([SECURE_HUB_KEY]);
    expect(await sh.load()).toEqual(hub);
    await sh.clear();
    expect(await sh.load()).toBeNull();
    expect(mem.dump()).toEqual({});
  });

  it('passes keychainAccessible on every call', async () => {
    const { s, calls } = recording(createMemorySecureStore());
    const sh = createSecureHub(s, secureOptions(KEYCHAIN_ACCESSIBLE));
    await sh.save(hub);
    await sh.load();
    await sh.clear();
    expect(calls.map((c) => c.op)).toEqual(['set', 'get', 'delete']);
    for (const c of calls) {
      expect(c.key).toBe(SECURE_HUB_KEY);
      expect(c.options).toEqual({ keychainAccessible: 'AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY' });
    }
  });

  it('asserts the option is present', () => {
    const mem = createMemorySecureStore();
    expect(() => createSecureHub(mem, {})).toThrow(/keychainAccessible/);
    expect(() => createSecureHub(mem, { keychainService: 'x' })).toThrow(/keychainAccessible/);
    // a numeric constant (iOS) counts, including 0
    expect(() => createSecureHub(mem, secureOptions(0))).not.toThrow();
    expect(() => createSecureHub(mem, secureOptions(1))).not.toThrow();
  });

  it('treats a damaged entry as no pairing', async () => {
    for (const bad of ['', 'not json', '[]', 'null', '{"origin":"https://h"}', '{"token":"t"}', '{"origin":1,"token":"t"}']) {
      const mem = createMemorySecureStore();
      await mem.setItemAsync(SECURE_HUB_KEY, bad);
      expect(await createSecureHub(mem, secureOptions(1)).load()).toBeNull();
    }
  });

  it('fills missing optional fields', async () => {
    const mem = createMemorySecureStore();
    await mem.setItemAsync(SECURE_HUB_KEY, '{"origin":"https://h","token":"t"}');
    expect(await createSecureHub(mem, secureOptions(1)).load()).toEqual({ origin: 'https://h', token: 't', device: '', name: '', paired_at: '' });
  });

  it('keeps unconfirmed unpairings under their own key with the same option, at most four', async () => {
    const mem = createMemorySecureStore();
    const r = recording(mem);
    const sh = createSecureHub(r.s, secureOptions(KEYCHAIN_ACCESSIBLE));
    expect(await sh.loadRevokes()).toEqual([]);
    const list = [1, 2, 3, 4, 5].map((i) => ({ origin: `https://h${i}.ts.net`, token: `t${i}` }));
    await sh.saveRevokes(list);
    expect(await sh.loadRevokes()).toEqual(list.slice(1));
    expect(r.calls.every((c) => c.options?.keychainAccessible === KEYCHAIN_ACCESSIBLE)).toBe(true);
    expect(r.calls.some((c) => c.key === SECURE_REVOKE_KEY)).toBe(true);
    await sh.save(hub);
    await sh.clear(); // clearing the pairing keeps the unpairings to retry
    expect(await sh.loadRevokes()).toHaveLength(4);
    await sh.saveRevokes([]);
    expect(mem.dump()).toEqual({});
    await mem.setItemAsync(SECURE_REVOKE_KEY, '{"not":"a list"}');
    expect(await sh.loadRevokes()).toEqual([]);
  });

  it('a locked store throws on read and write (the locked-store fake)', async () => {
    const mem = createMemorySecureStore({ locked: true });
    const sh = createSecureHub(mem, secureOptions(1));
    await expect(sh.load()).rejects.toThrow();
    await expect(sh.save(hub)).rejects.toThrow();
  });
});

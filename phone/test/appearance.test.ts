// The theme setting (Settings, Appearance): device default unless chosen, saved locally,
// applied app-wide through Appearance.setColorScheme.
import { Appearance } from 'react-native';
import { loadTheme, parseTheme, saveTheme, THEME_KEY } from '../src/components/appearance';

function kv(initial: Record<string, string> = {}) {
  const m = new Map(Object.entries(initial));
  return {
    m,
    getItem: async (k: string) => m.get(k) ?? null,
    setItem: async (k: string, v: string) => void m.set(k, v),
    removeItem: async (k: string) => void m.delete(k),
  };
}

describe('appearance', () => {
  let set: jest.SpyInstance;
  beforeEach(() => {
    set = jest.spyOn(Appearance, 'setColorScheme').mockImplementation(() => {});
  });
  afterEach(() => set.mockRestore());

  it('is device default when nothing or junk is saved', async () => {
    expect(await loadTheme(kv())).toBe('system');
    expect(set).toHaveBeenLastCalledWith('unspecified');
    expect(await loadTheme(kv({ [THEME_KEY]: 'purple' }))).toBe('system');
    expect(parseTheme(null)).toBe('system');
  });

  it('applies and saves light and dark, and back to device default', async () => {
    const store = kv();
    await saveTheme(store, 'dark');
    expect(set).toHaveBeenLastCalledWith('dark');
    expect(store.m.get(THEME_KEY)).toBe('dark');
    expect(await loadTheme(store)).toBe('dark');
    await saveTheme(store, 'light');
    expect(set).toHaveBeenLastCalledWith('light');
    await saveTheme(store, 'system');
    expect(set).toHaveBeenLastCalledWith('unspecified');
  });

  it('still applies the choice when storage fails', async () => {
    const broken = { getItem: async () => { throw new Error('x'); }, setItem: async () => { throw new Error('x'); }, removeItem: async () => {} };
    await saveTheme(broken, 'dark');
    expect(set).toHaveBeenLastCalledWith('dark');
    expect(await loadTheme(broken)).toBe('system');
  });
});

// The sy-link JS API (push.md 14.8) against src/contract/link.ts, which mirrors it for src/lib
// (P1): the types must stay equal, and the module itself must satisfy LinkLike.
import * as SyLink from '../modules/sy-link';
import type * as C from '../src/contract/link';
import { LINK_ERR_HUB_KEY, LINK_ERR_TAMPERED } from '../src/contract/link';

type Eq<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;

const same: [
  Eq<C.LinkConfig, SyLink.LinkConfig>,
  Eq<C.LinkState, SyLink.LinkState>,
  Eq<C.PublicKeys, SyLink.PublicKeys>,
  Eq<C.LinkRefresh, SyLink.LinkRefresh>,
  Eq<C.LinkStatus, SyLink.LinkStatus>,
] = [true, true, true, true, true];

// Compile-time: the module's exports are a LinkLike.
const asLink: C.LinkLike = SyLink;

describe('sy-link contract', () => {
  it('keeps the module types and the contract mirror equal', () => {
    expect(same.every(Boolean)).toBe(true);
    expect(typeof asLink.configure).toBe('function');
  });

  it('names the same configure() refusal codes', () => {
    const real = jest.requireActual<typeof SyLink>('../modules/sy-link');
    expect(real.CONFIGURE_ERRORS).toEqual({ hubKey: LINK_ERR_HUB_KEY, tampered: LINK_ERR_TAMPERED });
  });

  it('rejects every call where the native module is missing (iOS, Expo Go)', async () => {
    const real = jest.requireActual<typeof SyLink>('../modules/sy-link');
    expect(real.available).toBe(false);
    await expect(real.getState()).rejects.toThrow('sy-link: Android only');
    await expect(real.prepareKeys()).rejects.toThrow('sy-link: Android only');
    await expect(real.getRefresh()).rejects.toThrow('sy-link: Android only');
    expect(() => real.addListener('state', () => {}).remove()).not.toThrow();
  });
});

// Settings' view of the updater (src/lib/update): its state as a React value.
import { useSyncExternalStore } from 'react';
import type { UpdateView, Updater } from '../lib/update';

const NONE: UpdateView = { phase: 'idle', info: null, checkedAt: null, message: null };
const noSubscribe = () => () => {};

export function useUpdate(u: Updater | undefined): UpdateView {
  return useSyncExternalStore(
    u ? u.subscribe : noSubscribe,
    u ? u.view : () => NONE,
    u ? u.view : () => NONE,
  );
}

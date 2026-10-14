// Thin adapter for the background notification task (phone.md P6.5). All logic is in
// src/lib/notify (runNotifyTask); this file only binds expo-task-manager and
// expo-notifications. It imports no React and no UI. Call defineNotifyTask at module scope
// (app/_layout.tsx), because the OS starts the JS bundle headless and the task must be
// defined before anything renders.
import * as Notifications from 'expo-notifications';
import * as TaskManager from 'expo-task-manager';
import { NOTIFY_TASK } from '../contract/constants';
import type { Clock, FetchLike, KVStore } from '../contract/platform';
import { runNotifyTask, type NotificationsLike, type TaskHub } from '../lib/notify';

export interface NotifyTaskDeps {
  /** SecureHub.load, narrowed: rejects while the keychain is locked. */
  loadHub(): Promise<TaskHub | null>;
  /** Refresh overview and accounts, widgets and Live Activity; the needs-you count. */
  refresh(hub: TaskHub): Promise<number | null>;
  fetch?: FetchLike;
  clock?: Clock;
  /** AsyncStorage: the acts already done, shared with the response listener (one act per tap). */
  acted?: KVStore;
}

const realClock: Clock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

/** Defines SY_NOTIFY_TASK. */
export function defineNotifyTask(deps: NotifyTaskDeps): void {
  const notifications = Notifications as unknown as NotificationsLike;
  TaskManager.defineTask(NOTIFY_TASK, async (body: unknown) => {
    await runNotifyTask(
      {
        notifications,
        fetch: deps.fetch ?? ((url, init) => (globalThis.fetch as unknown as FetchLike)(url, init)),
        clock: deps.clock ?? realClock,
        loadHub: deps.loadHub,
        refresh: deps.refresh,
        ...(deps.acted ? { acted: deps.acted } : {}),
      },
      body,
    );
  });
}

/** Registers the task with expo-notifications (after the permission is granted). */
export async function registerNotifyTask(): Promise<void> {
  await Notifications.registerTaskAsync(NOTIFY_TASK);
}

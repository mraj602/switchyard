// Entry points of the widgets and the Live Activity for the app (phone.md P11, P12). WP-I wires:
//   - app/_layout.tsx, module scope: `registerAndroidWidgets()` (Android task handler);
//   - after every data refresh (and in the background notification task):
//       const snap = buildSnapshot(overview, accounts, now, {private, origin});
//       updateTimelines(snap, now); await updateAndroidWidgets(snap);
//       await syncCountdown(snap, now, wantAutoStart);
//   - Accounts card "Count down on Lock Screen": countdownFor(snap, seatId, now) then
//     startCountdown(props, seatId);
//   - unpair / 401: await clearIos(now); await clearAndroidWidgets().
export * from './ios/adapter';
export * from './android/task-handler';
export { NeedsYouSmall } from './ios/NeedsYouSmall';
export { AccountsMedium } from './ios/AccountsMedium';
export { ResetCountdown } from './ios/ResetCountdown';
export { SyNeedsYou } from './android/SyNeedsYou';
export { SyAccounts } from './android/SyAccounts';

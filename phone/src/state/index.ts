// App state (phone.md P3 to P5). Owner: WP-P; the glue (WP-I) is ./glue.
// Screens read `useHubApi()` (src/components/hub-api.tsx); `useHub()` is the app core's state.
export { HubContext, HubTestProvider, useHub, useHubData } from './context';
export type { HubState, PairStatus } from './context';
export { HubProvider, UNPAIRED_ON_HUB } from './provider';
export type { HubProviderProps, ProviderDeps } from './provider';
export { stateHooks } from './hooks';
export type { StateHooks } from './hooks';
export { bridge } from './bridge';
export { HubGlue, installStateHooks, systemGluePlatform } from './glue';

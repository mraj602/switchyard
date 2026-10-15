// App routes (docs/spec/phone.md P4, P7). The push `url` (a PWA hash route) and incoming
// switchyard:// URLs both map onto AppRoute through src/lib/links.

export const SCHEME = 'switchyard';

/** Ids in routes and deep links (P7). */
export const ROUTE_ID = /^[A-Za-z0-9_.:-]{1,80}$/;

export type SessionFilter = 'needs_you' | 'working' | 'done' | 'all' | 'ended';

export type AppRoute =
  | { name: 'now' }
  | { name: 'sessions'; filter?: SessionFilter; by?: string }
  | { name: 'session'; id: string; alert?: string }
  | { name: 'accounts'; seat?: string }
  | { name: 'more' }
  | { name: 'alerts-settings' }
  | { name: 'machines'; machine?: string }
  | { name: 'settings' }
  /** PE3: the peers audit list, the matrix (narrowing only) and Pause all peers (peers.md 10, 11). */
  | { name: 'peers' }
  /** More, Analytics (analytics.md): cost by day, model and project; limit hits. */
  | { name: 'analytics' }
  /** P6.7 (N1): Settings → Notifications; the link's persistent notification opens it. */
  | { name: 'notify-settings' }
  | { name: 'alert'; id: string }
  | { name: 'pair'; origin?: string; secret?: string; code?: string; hubFp?: string; sealKey?: string };

/** expo-router paths of each route (P4 table). */
export const ROUTE_PATHS = {
  now: '/',
  sessions: '/sessions',
  session: '/sessions/[id]',
  accounts: '/accounts',
  more: '/more',
  'alerts-settings': '/more/alerts',
  machines: '/more/machines',
  settings: '/more/settings',
  peers: '/more/peers',
  analytics: '/more/analytics',
  'notify-settings': '/more/settings/notifications',
  alert: '/alerts/[id]',
  pair: '/pair',
} as const satisfies Record<AppRoute['name'], string>;

/** The hash route each app route mirrors (P4 table). */
export const HASH_ROUTES = {
  now: '#/',
  sessions: '#/sessions',
  session: '#/sessions/<id>',
  accounts: '#/accounts',
  more: '#/more',
  'alerts-settings': '#/more/alerts',
  machines: '#/machines',
  settings: '#/more/settings',
  peers: '#/more/peers',
  analytics: '#/more/analytics',
  'notify-settings': '#/more/settings', // no PWA screen of its own
  alert: '#/alerts/<id>',
  pair: '#/pair',
} as const satisfies Record<AppRoute['name'], string>;

/** Hosts of switchyard:// URLs the app accepts (P7 allow-list); everything else opens Now. */
export const INCOMING_HOSTS = ['now', 'sessions', 'accounts', 'machines', 'alerts', 'pair', 'settings'] as const;

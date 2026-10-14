// Navigation between the screens (P4's route table). Plain hrefs: the screens do not need
// WP-N's link mapping, only the alert redirect does.
import { router } from 'expo-router';

const enc = encodeURIComponent;

export const go = {
  session: (id: string, alert?: string) => router.push(`/sessions/${enc(id)}${alert ? `?alert=${enc(alert)}` : ''}` as never),
  sessions: () => router.push('/sessions' as never),
  accounts: (seat?: string) => router.push(`/accounts${seat ? `?seat=${enc(seat)}` : ''}` as never),
  machines: (machine?: string) => router.push(`/more/machines${machine ? `?machine=${enc(machine)}` : ''}` as never),
  alerts: () => router.push('/more/alerts' as never),
  settings: () => router.push('/more/settings' as never),
  now: () => router.replace('/' as never),
};

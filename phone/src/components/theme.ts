// Colours and spacing of the app's screens. Light and dark follow the system setting.
import { useColorScheme } from 'react-native';

export interface Theme {
  bg: string;
  card: string;
  text: string;
  muted: string;
  line: string;
  accent: string;
  accentText: string;
  ok: string;
  warn: string;
  bad: string;
  needs: string;
  track: string;
}

export const LIGHT: Theme = {
  bg: '#F6F7F8', card: '#FFFFFF', text: '#14171A', muted: '#5C646D', line: '#E1E4E8', accent: '#0E7C76', accentText: '#FFFFFF',
  ok: '#18794E', warn: '#9A6700', bad: '#C62828', needs: '#B45309', track: '#E3E6EA',
};

export const DARK: Theme = {
  bg: '#0F1214', card: '#1A1E21', text: '#ECEFF1', muted: '#9AA3AB', line: '#2B3136', accent: '#35B8AE', accentText: '#06201E',
  ok: '#4CC38A', warn: '#E5B53B', bad: '#FF7B72', needs: '#F2A154', track: '#2B3136',
};

export function useTheme(): Theme {
  return useColorScheme() === 'dark' ? DARK : LIGHT;
}

export const SPACE = { xs: 4, s: 8, m: 12, l: 16, xl: 24 } as const;

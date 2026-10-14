// Alert redirect (P4, push.md 5.8): `/alerts/[id]`.
import { useLocalSearchParams } from 'expo-router';
import AlertRedirectScreen from '../../src/components/screens/AlertRedirectScreen';

export default function Route() {
  const p = useLocalSearchParams<{ id: string }>();
  return <AlertRedirectScreen id={String(p.id)} />;
}

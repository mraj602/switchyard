// A claude.ai account tab (P17): `/tabs/[seat]?url=`.
import { useLocalSearchParams } from 'expo-router';
import ClaudeTabScreen from '../../src/components/screens/ClaudeTabScreen';

export default function Route() {
  const p = useLocalSearchParams<{ seat: string; url?: string }>();
  return <ClaudeTabScreen key={`${p.seat}|${p.url ?? ''}`} seat={String(p.seat)} url={p.url} />;
}

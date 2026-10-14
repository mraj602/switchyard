// Session (P4): `/sessions/[id]?alert=`.
import { useLocalSearchParams } from 'expo-router';
import SessionScreen from '../../src/components/screens/SessionScreen';

export default function Route() {
  const p = useLocalSearchParams<{ id: string; alert?: string }>();
  return <SessionScreen id={String(p.id)} alert={p.alert} />;
}

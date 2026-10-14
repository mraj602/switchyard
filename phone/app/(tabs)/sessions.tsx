// Tab Sessions (P4): `/sessions?filter=&by=&machine=`.
import { useLocalSearchParams } from 'expo-router';
import SessionsScreen from '../../src/components/screens/SessionsScreen';

export default function Route() {
  const p = useLocalSearchParams<{ filter?: string; by?: string; machine?: string }>();
  return <SessionsScreen key={`${p.filter}|${p.by}|${p.machine}`} filter={p.filter} by={p.by} machine={p.machine} />;
}

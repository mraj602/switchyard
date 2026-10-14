// Machines (P4): `/more/machines?machine=`.
import { useLocalSearchParams } from 'expo-router';
import MachinesScreen from '../../src/components/screens/MachinesScreen';

export default function Route() {
  const p = useLocalSearchParams<{ machine?: string }>();
  return <MachinesScreen machine={p.machine} />;
}

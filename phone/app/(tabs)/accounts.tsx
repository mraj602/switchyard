// Tab Accounts (P4): `/accounts?seat=`.
import { useLocalSearchParams } from 'expo-router';
import AccountsScreen from '../../src/components/screens/AccountsScreen';

export default function Route() {
  const p = useLocalSearchParams<{ seat?: string }>();
  return <AccountsScreen seat={p.seat} />;
}

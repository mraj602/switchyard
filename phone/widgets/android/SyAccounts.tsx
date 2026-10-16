// Android 4x2 widget (phone.md P11): up to four accounts, each a usage bar with its used %, its
// other window and the reset as a clock time ("resets 17:00"; no ticking timer on Android).
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { clock, resetKeysAt, seatRows, type AndroidWidgetProps, type SeatRow } from '../../src/lib/widget';

const BG = '#17191B'; // the brand ink (theme.ts, the launcher icon)
const FG = '#ECEEEF';
const DIM = '#9AA1A8';
const TRACK = '#2A2E32';
const BAR_W = 64;

type Hex = `#${string}`;

function Column({ row, tz }: { row: SeatRow; tz?: string }) {
  const fill = Math.max(2, Math.round((BAR_W * row.pct) / 100));
  return (
    <FlexWidget style={{ flex: 1, flexDirection: 'column', paddingHorizontal: 4 }} clickAction="OPEN_URI" clickActionData={{ uri: `switchyard://accounts?seat=${encodeURIComponent(row.id)}` }}>
      <TextWidget text={row.label} maxLines={1} truncate="END" style={{ fontSize: 12, fontWeight: 'bold', color: row.colour as Hex }} />
      <FlexWidget style={{ width: BAR_W, height: 6, borderRadius: 3, backgroundColor: TRACK, marginVertical: 4 }}>
        <FlexWidget style={{ width: fill, height: 6, borderRadius: 3, backgroundColor: row.colour as Hex }} />
      </FlexWidget>
      <TextWidget text={`${row.pct}%`} style={{ fontSize: 16, fontWeight: 'bold', color: FG }} />
      <TextWidget text={row.reset || !row.resets_at ? 'reset' : `resets ${clock(row.resets_at, tz)}`} maxLines={1} style={{ fontSize: 10, color: DIM }} />
      {row.other ? <TextWidget text={`${row.other.window === 'seven_day' ? '7d' : '5h'} ${row.other.pct}%`} style={{ fontSize: 10, color: DIM }} /> : null}
    </FlexWidget>
  );
}

export function SyAccounts({ snapshot, now, tz }: AndroidWidgetProps) {
  const root = { height: 'match_parent', width: 'match_parent', backgroundColor: BG, borderRadius: 16, padding: 12 } as const;
  if (!snapshot) {
    return (
      <FlexWidget style={root} clickAction="OPEN_URI" clickActionData={{ uri: 'switchyard://now' }}>
        <TextWidget text="Switchyard" style={{ fontSize: 14, fontWeight: 'bold', color: FG }} />
        <TextWidget text="Open Switchyard to set up" style={{ fontSize: 12, color: DIM }} />
      </FlexWidget>
    );
  }
  const rows = seatRows(snapshot, resetKeysAt(snapshot, now));
  return (
    <FlexWidget style={root} clickAction="OPEN_URI" clickActionData={{ uri: 'switchyard://accounts' }}>
      <TextWidget text="Accounts" style={{ fontSize: 12, color: DIM }} />
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', marginTop: 6 }}>
        {rows.map((r) => (
          <Column key={r.id} row={r} {...(tz ? { tz } : {})} />
        ))}
      </FlexWidget>
    </FlexWidget>
  );
}

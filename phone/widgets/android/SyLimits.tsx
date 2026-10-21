// Android Limits widget (phone.md P11): every visible account with its colour, the tighter of
// 5 h and 7 d as a bar and %, and that window's reset as a clock time. An idle account is 0%.
// Resizable; draws as many rows as the height allows (rowsThatFit). Props in, tree out.
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { agoWords, clock, limitRows, rowsThatFit, type AndroidWidgetProps, type LimitRow } from '../../src/lib/widget';

const BG = '#17191B'; // the brand ink (theme.ts, the launcher icon)
const FG = '#ECEEEF';
const DIM = '#9AA1A8';
const TRACK = '#2A2E32';

type Hex = `#${string}`;

export const EMPTY_TEXT = 'Open Switchyard once to load';

function resetText(r: LimitRow, tz?: string): string {
  if (r.idle) return 'not started';
  if (r.reset) return 'reset';
  if (!r.resets_at || !r.window) return 'not started';
  return `${r.window} · ${clock(r.resets_at, tz)}`;
}

function Row({ row, tz }: { row: LimitRow; tz?: string }) {
  const fill = Math.max(0, Math.min(100, row.pct));
  return (
    <FlexWidget
      style={{ flexDirection: 'row', alignItems: 'center', width: 'match_parent', marginTop: 6 }}
      clickAction="OPEN_URI"
      clickActionData={{ uri: `switchyard://accounts?seat=${encodeURIComponent(row.id)}` }}
    >
      <TextWidget text={row.label} maxLines={1} truncate="END" style={{ width: 72, fontSize: 12, fontWeight: 'bold', color: row.colour as Hex }} />
      <FlexWidget style={{ flex: 1, flexDirection: 'row', height: 6, borderRadius: 3, backgroundColor: TRACK, marginHorizontal: 6 }}>
        {fill > 0 ? <FlexWidget style={{ flex: fill, height: 6, borderRadius: 3, backgroundColor: row.colour as Hex }} /> : null}
        {fill < 100 ? <FlexWidget style={{ flex: 100 - fill, height: 6 }} /> : null}
      </FlexWidget>
      <TextWidget text={`${row.pct}%`} style={{ width: 34, fontSize: 12, fontWeight: 'bold', color: FG }} />
      <TextWidget text={resetText(row, tz)} maxLines={1} style={{ width: 66, fontSize: 10, color: DIM }} />
    </FlexWidget>
  );
}

export function SyLimits({ snapshot, now, tz, height }: AndroidWidgetProps) {
  const root = { height: 'match_parent', width: 'match_parent', backgroundColor: BG, borderRadius: 16, padding: 12 } as const;
  if (!snapshot) {
    return (
      <FlexWidget style={root} clickAction="OPEN_URI" clickActionData={{ uri: 'switchyard://now' }}>
        <TextWidget text="Limits" style={{ fontSize: 14, fontWeight: 'bold', color: FG }} />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM }} />
      </FlexWidget>
    );
  }
  const all = limitRows(snapshot, now, tz);
  const rows = all.slice(0, rowsThatFit(height, all.length));
  return (
    <FlexWidget style={root} clickAction="OPEN_URI" clickActionData={{ uri: 'switchyard://accounts' }}>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', justifyContent: 'space-between' }}>
        <TextWidget text="Limits" style={{ fontSize: 12, fontWeight: 'bold', color: FG }} />
        <TextWidget text={`updated ${agoWords(snapshot.as_of, now)}`} maxLines={1} style={{ fontSize: 10, color: DIM }} />
      </FlexWidget>
      {rows.length ? rows.map((r) => <Row key={r.id} row={r} {...(tz ? { tz } : {})} />) : <TextWidget text="No accounts yet" style={{ fontSize: 12, color: DIM }} />}
    </FlexWidget>
  );
}

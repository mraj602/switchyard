// Android Limits widget (phone.md P11): every visible account with its colour, the selected
// window (the 5 h | 7 d toggle in the header, per widget) as a bar and %, and that window's
// reset ("resets 15:48"; an idle window is "0% · not started"). Resizable; draws as many rows
// as the height allows (rowsThatFit). Props in, tree out. Tap a row: that account; elsewhere:
// Accounts; the toggle redraws the widget without opening the app.
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { agoWords, rowsThatFit, type AndroidWidgetProps } from '../../src/lib/widget';
import { windowRows, type WindowRow } from '../../src/lib/widget/extra';
import { Brand, WindowToggle } from './Brand';
import { DIM, EMPTY_TEXT, FG, ROOT, TRACK, type Hex } from './common';

export { EMPTY_TEXT };

function Row({ row }: { row: WindowRow }) {
  const fill = Math.max(0, Math.min(100, row.pct));
  return (
    <FlexWidget
      style={{ flexDirection: 'row', alignItems: 'center', width: 'match_parent', marginTop: 6 }}
      clickAction="OPEN_URI"
      clickActionData={{ uri: `watchyard://accounts?seat=${encodeURIComponent(row.id)}` }}
    >
      <TextWidget text={row.label} maxLines={1} truncate="END" style={{ width: 72, fontSize: 12, fontWeight: 'bold', color: row.colour as Hex }} />
      <FlexWidget style={{ flex: 1, flexDirection: 'row', height: 6, borderRadius: 3, backgroundColor: TRACK, marginHorizontal: 6 }}>
        {fill > 0 ? <FlexWidget style={{ flex: fill, height: 6, borderRadius: 3, backgroundColor: row.colour as Hex }} /> : null}
        {fill < 100 ? <FlexWidget style={{ flex: 100 - fill, height: 6 }} /> : null}
      </FlexWidget>
      <TextWidget text={`${row.pct}%`} style={{ width: 34, fontSize: 12, fontWeight: 'bold', color: FG }} />
      <TextWidget text={row.words} maxLines={1} style={{ width: 72, fontSize: 10, color: DIM }} />
    </FlexWidget>
  );
}

export function WyLimits({ snapshot, now, tz, height, win }: AndroidWidgetProps) {
  const selected = win ?? '5 h';
  if (!snapshot) {
    return (
      <FlexWidget style={ROOT} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://now' }}>
        <Brand title="Watchyard · Limits" />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM, marginTop: 4 }} />
      </FlexWidget>
    );
  }
  const all = windowRows(snapshot, selected, now, tz);
  const rows = all.slice(0, rowsThatFit(height, all.length));
  return (
    <FlexWidget style={ROOT} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://accounts' }}>
      <FlexWidget style={{ flexDirection: 'row', alignItems: 'center', width: 'match_parent', justifyContent: 'space-between' }}>
        <Brand title="Watchyard · Limits" />
        <TextWidget text={`updated ${agoWords(snapshot.as_of, now)}`} maxLines={1} style={{ fontSize: 9, color: DIM }} />
        <WindowToggle selected={selected} />
      </FlexWidget>
      {rows.length ? rows.map((r) => <Row key={r.id} row={r} />) : <TextWidget text="No accounts yet" style={{ fontSize: 12, color: DIM, marginTop: 6 }} />}
    </FlexWidget>
  );
}

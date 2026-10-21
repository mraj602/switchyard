// Android Account ring widget (phone.md P11): one account, chosen when the widget is placed
// (and again from "Reconfigure"): a ring of the tighter of 5 h and 7 d with the %, the window,
// and when it resets. react-native-android-widget has no Chronometer or other ticking view, so
// the reset reads as a clock time ("resets 15:48") and an alarm redraws it at the reset
// (sy-link scheduleWidgetRefresh, WidgetTick.kt). An idle account is "0 %" "not started".
import { FlexWidget, OverlapWidget, SvgWidget, TextWidget } from 'react-native-android-widget';

import type { AndroidWidgetProps } from '../../src/lib/widget';
import { ringModel, ringSvg } from '../../src/lib/widget/extra';
import { DIM, EMPTY_TEXT, FG, ROOT, type Hex } from './common';

export function SyRing({ snapshot, now, tz, seat }: AndroidWidgetProps) {
  const open = (uri: string) => ({ clickAction: 'OPEN_URI' as const, clickActionData: { uri } });
  if (!snapshot) {
    return (
      <FlexWidget style={ROOT} {...open('switchyard://now')}>
        <TextWidget text="Account" style={{ fontSize: 14, fontWeight: 'bold', color: FG }} />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM }} />
      </FlexWidget>
    );
  }
  const m = ringModel(snapshot, seat, now, tz);
  if (!m) {
    return (
      <FlexWidget style={ROOT} {...open('switchyard://accounts')}>
        <TextWidget text="Account" style={{ fontSize: 14, fontWeight: 'bold', color: FG }} />
        <TextWidget text="Account not found. Hold the widget and choose Reconfigure." style={{ fontSize: 12, color: DIM }} />
      </FlexWidget>
    );
  }
  const href = seat ? `switchyard://accounts?seat=${encodeURIComponent(seat)}` : 'switchyard://accounts';
  return (
    <FlexWidget style={{ ...ROOT, padding: 10, alignItems: 'center' }} {...open(href)}>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', justifyContent: 'space-between' }}>
        <TextWidget text={m.label} maxLines={1} truncate="END" style={{ fontSize: 11, fontWeight: 'bold', color: m.colour as Hex }} />
        <TextWidget text={m.window ?? ''} style={{ fontSize: 11, color: DIM }} />
      </FlexWidget>
      <OverlapWidget style={{ width: 84, height: 84, marginTop: 4 }}>
        <SvgWidget svg={ringSvg(m.pct, m.colour, 84, 9)} style={{ width: 84, height: 84 }} />
        <FlexWidget style={{ width: 'match_parent', height: 'match_parent', alignItems: 'center', justifyContent: 'center' }}>
          <TextWidget text={`${m.pct} %`} style={{ fontSize: 17, fontWeight: 'bold', color: FG }} />
        </FlexWidget>
      </OverlapWidget>
      <TextWidget text={m.caption} maxLines={1} style={{ fontSize: 11, color: DIM, marginTop: 2 }} />
    </FlexWidget>
  );
}

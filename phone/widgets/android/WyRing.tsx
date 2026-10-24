// Android Account ring widget (phone.md P11): one account, chosen when the widget is placed
// (and again from "Reconfigure"): a ring of the selected window (the 5 h | 7 d toggle, kept per
// widget) with the %, and when it resets. react-native-android-widget has no Chronometer or
// other ticking view, so the reset reads as a clock time ("resets 15:48") and an alarm redraws
// it at the reset (wy-link scheduleWidgetRefresh, WidgetTick.kt). An idle window is "0 %" "not
// started". Header: the Watchyard mark and name, then the account and the toggle.
import { FlexWidget, OverlapWidget, SvgWidget, TextWidget } from 'react-native-android-widget';

import type { AndroidWidgetProps } from '../../src/lib/widget';
import { ringModel, ringSvg } from '../../src/lib/widget/extra';
import { Brand, WindowToggle } from './Brand';
import { DIM, EMPTY_TEXT, FG, ROOT, type Hex } from './common';

export function WyRing({ snapshot, now, tz, seat, win }: AndroidWidgetProps) {
  const open = (uri: string) => ({ clickAction: 'OPEN_URI' as const, clickActionData: { uri } });
  const selected = win ?? '5 h';
  if (!snapshot) {
    return (
      <FlexWidget style={ROOT} {...open('watchyard://now')}>
        <Brand title="Watchyard" />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM, marginTop: 4 }} />
      </FlexWidget>
    );
  }
  const m = ringModel(snapshot, seat, now, tz, selected);
  if (!m) {
    return (
      <FlexWidget style={ROOT} {...open('watchyard://accounts')}>
        <Brand title="Watchyard" />
        <TextWidget text="Account not found. Hold the widget and choose Reconfigure." style={{ fontSize: 12, color: DIM, marginTop: 4 }} />
      </FlexWidget>
    );
  }
  const href = seat ? `watchyard://accounts?seat=${encodeURIComponent(seat)}` : 'watchyard://accounts';
  return (
    <FlexWidget style={{ ...ROOT, padding: 8, alignItems: 'center' }} {...open(href)}>
      <FlexWidget style={{ width: 'match_parent' }}>
        <Brand title="Watchyard" />
      </FlexWidget>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', alignItems: 'center', justifyContent: 'space-between', marginTop: 3 }}>
        <TextWidget text={m.label} maxLines={1} truncate="END" style={{ fontSize: 11, fontWeight: 'bold', color: m.colour as Hex }} />
        <WindowToggle selected={selected} />
      </FlexWidget>
      <OverlapWidget style={{ width: 58, height: 58, marginTop: 4 }}>
        <SvgWidget svg={ringSvg(m.pct, m.colour, 58, 7)} style={{ width: 58, height: 58 }} />
        <FlexWidget style={{ width: 'match_parent', height: 'match_parent', alignItems: 'center', justifyContent: 'center' }}>
          <TextWidget text={`${m.pct} %`} style={{ fontSize: 14, fontWeight: 'bold', color: FG }} />
        </FlexWidget>
      </OverlapWidget>
      <TextWidget text={m.caption} maxLines={1} style={{ fontSize: 10, color: DIM, marginTop: 2 }} />
    </FlexWidget>
  );
}

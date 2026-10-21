// Android Next reset widget (phone.md P11): the soonest limit reset across accounts, as
// "Next reset · claude-2 · 5 h" and the time ("15:48", or "Thu 09:00" when not today). No ticking
// countdown (the widget library has none): an alarm redraws it at the reset (see SyRing).
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import type { AndroidWidgetProps } from '../../src/lib/widget';
import { nextResetModel } from '../../src/lib/widget/extra';
import { DIM, EMPTY_TEXT, FG, ROOT } from './common';

export function SyNextReset({ snapshot, now, tz }: AndroidWidgetProps) {
  const open = { clickAction: 'OPEN_URI' as const, clickActionData: { uri: 'switchyard://accounts' } };
  if (!snapshot) {
    return (
      <FlexWidget style={ROOT} {...open}>
        <TextWidget text="Next reset" style={{ fontSize: 12, fontWeight: 'bold', color: FG }} />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM }} />
      </FlexWidget>
    );
  }
  const n = nextResetModel(snapshot, now, tz);
  return (
    <FlexWidget style={ROOT} {...open}>
      <TextWidget text={n ? `Next reset · ${n.seat} · ${n.window}` : 'Next reset'} maxLines={1} truncate="END" style={{ fontSize: 11, color: DIM }} />
      <TextWidget text={n ? n.at : 'None ahead'} maxLines={1} style={{ fontSize: n ? 26 : 16, fontWeight: 'bold', color: n ? FG : DIM, marginTop: 2 }} />
    </FlexWidget>
  );
}

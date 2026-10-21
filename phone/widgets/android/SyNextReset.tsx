// Android Next reset widget (phone.md P11): the soonest limit reset across accounts, as the
// time ("15:48", or "Thu 09:00" when not today) and "claude-2 · 5 h". No ticking countdown (the
// widget library has none): an alarm redraws it at the reset (see SyRing). Compact on 2x1: the
// Switchyard mark and name, then the time and the account; wider widgets say "Switchyard · Next
// reset".
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import type { AndroidWidgetProps } from '../../src/lib/widget';
import { nextResetModel } from '../../src/lib/widget/extra';
import { Brand } from './Brand';
import { DIM, EMPTY_TEXT, FG, ROOT } from './common';

export function SyNextReset({ snapshot, now, tz, width }: AndroidWidgetProps) {
  const open = { clickAction: 'OPEN_URI' as const, clickActionData: { uri: 'switchyard://accounts' } };
  const wide = !width || width >= 200;
  const root = { ...ROOT, padding: 8 };
  if (!snapshot) {
    return (
      <FlexWidget style={root} {...open}>
        <Brand title="Switchyard" />
        <TextWidget text={EMPTY_TEXT} maxLines={1} style={{ fontSize: 10, color: DIM, marginTop: 2 }} />
      </FlexWidget>
    );
  }
  const n = nextResetModel(snapshot, now, tz);
  return (
    <FlexWidget style={root} {...open}>
      <Brand title={wide ? 'Switchyard · Next reset' : 'Switchyard'} />
      <FlexWidget style={{ flexDirection: 'row', alignItems: 'center', width: 'match_parent', marginTop: 2 }}>
        <TextWidget text={n ? n.at : 'None ahead'} maxLines={1} style={{ fontSize: n ? 20 : 14, fontWeight: 'bold', color: n ? FG : DIM }} />
        {n ? <TextWidget text={`  ${n.seat} · ${n.window}`} maxLines={1} truncate="END" style={{ fontSize: 10, color: DIM }} /> : null}
      </FlexWidget>
    </FlexWidget>
  );
}

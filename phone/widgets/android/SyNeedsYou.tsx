// Android 2x2 widget (phone.md P11): needs-you count, the top item and the next reset as a
// clock time ("resets 17:00"). Plain react-native-android-widget JSX: props in, tree out.
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { clock, nextResetAt, type AndroidWidgetProps } from '../../src/lib/widget';

const BG = '#17191B'; // the brand ink (theme.ts, the launcher icon)
const FG = '#ECEEEF';
const DIM = '#9AA1A8';
const OPEN = { clickAction: 'OPEN_URI', clickActionData: { uri: 'switchyard://now' } } as const;

export function SyNeedsYou({ snapshot, now, tz }: AndroidWidgetProps) {
  const root = { height: 'match_parent', width: 'match_parent', backgroundColor: BG, borderRadius: 16, padding: 12 } as const;
  if (!snapshot) {
    return (
      <FlexWidget style={root} {...OPEN}>
        <TextWidget text="Switchyard" style={{ fontSize: 14, fontWeight: 'bold', color: FG }} />
        <TextWidget text="Open Switchyard to set up" style={{ fontSize: 12, color: DIM }} />
      </FlexWidget>
    );
  }
  const top = snapshot.top;
  const next = nextResetAt(snapshot, now);
  return (
    <FlexWidget style={root} {...OPEN}>
      <TextWidget text="Needs you" style={{ fontSize: 12, color: DIM }} />
      <TextWidget text={String(snapshot.needs_you)} style={{ fontSize: 36, fontWeight: 'bold', color: snapshot.needs_you > 0 ? '#ff9f0a' : DIM }} />
      {top ? <TextWidget text={`${top.project} · ${top.what}`} maxLines={1} truncate="END" style={{ fontSize: 12, color: FG }} /> : null}
      {next ? <TextWidget text={`${next.seat} resets ${clock(next.resets_at, tz)}`} maxLines={1} truncate="END" style={{ fontSize: 11, color: DIM }} /> : null}
    </FlexWidget>
  );
}

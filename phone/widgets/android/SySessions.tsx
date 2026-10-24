// Android Sessions widget (phone.md P11): Running (working), Needs input (asked, permission,
// failed) and Done (finished a turn, idle) as the Now and Sessions screens count them, and a
// "needs you" highlight. Tap: Sessions; the needs-you cell and highlight open Now.
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { agoWords, sessionTally, type AndroidWidgetProps } from '../../src/lib/widget';
import { Brand } from './Brand';

const BG = '#17191B';
const FG = '#ECEEEF';
const DIM = '#9AA1A8';
const NEEDS = '#FF9F0A';
const WORKING = '#4FC38A';

export const EMPTY_TEXT = 'Open Watchyard once to load';

function Cell({ n, label, colour, uri }: { n: number; label: string; colour: `#${string}`; uri: string }) {
  return (
    <FlexWidget style={{ flex: 1, flexDirection: 'column', alignItems: 'center' }} clickAction="OPEN_URI" clickActionData={{ uri }}>
      <TextWidget text={String(n)} style={{ fontSize: 28, fontWeight: 'bold', color: n > 0 ? colour : DIM }} />
      <TextWidget text={label} maxLines={1} style={{ fontSize: 11, color: DIM }} />
    </FlexWidget>
  );
}

export function SySessions({ snapshot, now, height }: AndroidWidgetProps) {
  const root = { height: 'match_parent', width: 'match_parent', backgroundColor: BG, borderRadius: 16, padding: 12 } as const;
  if (!snapshot) {
    return (
      <FlexWidget style={root} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://now' }}>
        <Brand title="Watchyard · Sessions" />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM, marginTop: 4 }} />
      </FlexWidget>
    );
  }
  const t = sessionTally(snapshot);
  const top = snapshot.top;
  const roomy = !height || height >= 110;
  return (
    <FlexWidget style={root} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://sessions' }}>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', justifyContent: 'space-between' }}>
        <Brand title="Watchyard · Sessions" />
        <TextWidget text={`updated ${agoWords(snapshot.as_of, now)}`} maxLines={1} style={{ fontSize: 10, color: DIM }} />
      </FlexWidget>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', marginTop: 6 }}>
        <Cell n={t.running} label="Running" colour={WORKING} uri="watchyard://sessions?filter=working" />
        <Cell n={t.needsInput} label="Needs input" colour={NEEDS} uri="watchyard://now" />
        <Cell n={t.done} label="Done" colour={FG} uri="watchyard://sessions?filter=done" />
      </FlexWidget>
      {roomy && t.needsInput > 0 ? (
        <FlexWidget style={{ width: 'match_parent', marginTop: 8, padding: 6, borderRadius: 8, backgroundColor: '#3A2A0E' }} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://now' }}>
          <TextWidget
            text={top ? `Needs you: ${top.project} · ${top.what}` : `Needs you: ${t.needsInput} waiting`}
            maxLines={1}
            truncate="END"
            style={{ fontSize: 12, fontWeight: 'bold', color: NEEDS }}
          />
        </FlexWidget>
      ) : null}
    </FlexWidget>
  );
}

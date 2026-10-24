// Android Needs-you list widget (phone.md P11): the top 3 waiting sessions, oldest first, each
// as its account colour, "account · project · state" and its age. A row opens that session.
// Private mode keeps no list in the snapshot, so it shows the count only.
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { agoWords, type AndroidWidgetProps } from '../../src/lib/widget';
import { Brand } from './Brand';
import { needsRows, needsRowsThatFit } from '../../src/lib/widget/extra';
import { BG, DIM, EMPTY_TEXT, FG, NEEDS, ROOT, type Hex } from './common';

export const EMPTY_LIST = 'Nothing needs you';

export function WyNeeds({ snapshot, now, height }: AndroidWidgetProps) {
  if (!snapshot) {
    return (
      <FlexWidget style={ROOT} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://now' }}>
        <Brand title="Watchyard · Needs you" />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM, marginTop: 4 }} />
      </FlexWidget>
    );
  }
  const waiting = snapshot.counts?.needs_you ?? snapshot.needs_you;
  const rows = needsRows(snapshot, now, needsRowsThatFit(height));
  return (
    <FlexWidget style={ROOT} clickAction="OPEN_URI" clickActionData={{ uri: 'watchyard://now' }}>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', justifyContent: 'space-between' }}>
        <Brand title="Watchyard · Needs you" />
        <TextWidget text={waiting > 0 ? `${waiting} waiting` : 'all clear'} maxLines={1} style={{ fontSize: 11, fontWeight: 'bold', color: waiting > 0 ? NEEDS : DIM }} />
        <TextWidget text={`updated ${agoWords(snapshot.as_of, now)}`} maxLines={1} style={{ fontSize: 9, color: DIM }} />
      </FlexWidget>
      {waiting === 0 ? (
        <TextWidget text={EMPTY_LIST} style={{ fontSize: 13, color: DIM, marginTop: 10 }} />
      ) : rows.length === 0 ? (
        <TextWidget text="Open Watchyard to see which" style={{ fontSize: 12, color: DIM, marginTop: 10 }} />
      ) : (
        rows.map((r) => (
          <FlexWidget
            key={r.id}
            style={{ flexDirection: 'row', alignItems: 'center', width: 'match_parent', marginTop: 8 }}
            clickAction="OPEN_URI"
            clickActionData={{ uri: `watchyard://sessions/${encodeURIComponent(r.id)}` }}
          >
            <FlexWidget style={{ width: 8, height: 8, borderRadius: 4, backgroundColor: (r.colour || BG) as Hex, marginRight: 8 }} />
            <FlexWidget style={{ flex: 1 }}>
              <TextWidget text={`${r.seat} · ${r.project} · ${r.state}`} maxLines={1} truncate="END" style={{ fontSize: 12, fontWeight: 'bold', color: FG }} />
            </FlexWidget>
            <TextWidget text={r.age} maxLines={1} style={{ fontSize: 11, color: DIM, marginLeft: 6 }} />
          </FlexWidget>
        ))
      )}
    </FlexWidget>
  );
}

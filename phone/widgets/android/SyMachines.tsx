// Android Machines widget (phone.md P11): each machine with an online dot and "2 live" or
// "silent 1 h" under a "Machines · 2 of 3 online" header. As many as the width and height fit.
// Tap: Machines.
import { FlexWidget, TextWidget } from 'react-native-android-widget';

import { agoWords, type AndroidWidgetProps } from '../../src/lib/widget';
import { Brand } from './Brand';
import { machineGrid, machineRows, machinesHeader } from '../../src/lib/widget/extra';
import { DIM, EMPTY_TEXT, FG, OFFLINE, ONLINE, ROOT } from './common';

export function SyMachines({ snapshot, now, width, height }: AndroidWidgetProps) {
  const open = { clickAction: 'OPEN_URI' as const, clickActionData: { uri: 'switchyard://machines' } };
  if (!snapshot) {
    return (
      <FlexWidget style={ROOT} {...open}>
        <Brand title="Switchyard · Machines" />
        <TextWidget text={EMPTY_TEXT} style={{ fontSize: 12, color: DIM, marginTop: 4 }} />
      </FlexWidget>
    );
  }
  const all = machineRows(snapshot, now);
  const { perLine, shown } = machineGrid(width, height, all.length);
  const lines: (typeof all)[] = [];
  for (let i = 0; i < shown; i += perLine) lines.push(all.slice(i, Math.min(i + perLine, shown)));
  return (
    <FlexWidget style={ROOT} {...open}>
      <FlexWidget style={{ flexDirection: 'row', width: 'match_parent', justifyContent: 'space-between' }}>
        <Brand title="Switchyard" />
        <TextWidget text={all.length ? machinesHeader(snapshot) : 'Machines'} maxLines={1} style={{ fontSize: 11, fontWeight: 'bold', color: FG }} />
        <TextWidget text={`updated ${agoWords(snapshot.as_of, now)}`} maxLines={1} style={{ fontSize: 9, color: DIM }} />
      </FlexWidget>
      {all.length === 0 ? <TextWidget text="No machines yet" style={{ fontSize: 12, color: DIM, marginTop: 6 }} /> : null}
      {lines.map((line, i) => (
        <FlexWidget key={i} style={{ flexDirection: 'row', width: 'match_parent', marginTop: 6 }}>
          {line.map((m) => (
            <FlexWidget key={m.id} style={{ flex: 1, flexDirection: 'row', alignItems: 'center' }}>
              <FlexWidget style={{ width: 7, height: 7, borderRadius: 4, backgroundColor: m.online ? ONLINE : OFFLINE, marginRight: 5 }} />
              <TextWidget text={m.name} maxLines={1} truncate="END" style={{ fontSize: 12, fontWeight: 'bold', color: FG }} />
              <TextWidget text={` ${m.note}`} maxLines={1} style={{ fontSize: 10, color: m.online ? DIM : OFFLINE }} />
            </FlexWidget>
          ))}
        </FlexWidget>
      ))}
    </FlexWidget>
  );
}

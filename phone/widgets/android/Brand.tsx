// The brand header of every Android widget (phone.md P11): the gold double-chevron mark (the
// app's mark, assets/src/mark.svg, drawn with SvgWidget) and "Switchyard", or "Switchyard ·
// Limits" where there is room. Plus the 5 h | 7 d toggle of the Limits and ring widgets.
import { FlexWidget, SvgWidget, TextWidget } from 'react-native-android-widget';

import type { LimitWindow } from '../../src/lib/widget/extra';
import { ACCENT, DIM, FG, INK, TRACK } from './common';

/** assets/src/mark.svg, inline: the widget has no asset pipeline of its own. */
export const MARK_SVG =
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="1 0 20 20" width="20" height="20"><g fill="none" stroke="${ACCENT}" stroke-width="2.2" stroke-linejoin="miter" stroke-linecap="butt"><path d="M3 18 L11 4 L19 18"/><path d="M7 18 L11 11 L15 18"/></g></svg>`;

export function Brand({ title }: { title: string }) {
  return (
    <FlexWidget style={{ flexDirection: 'row', alignItems: 'center' }}>
      <SvgWidget svg={MARK_SVG} style={{ width: 14, height: 14, marginRight: 5 }} />
      <TextWidget text={title} maxLines={1} truncate="END" style={{ fontSize: 11, fontWeight: 'bold', color: FG }} />
    </FlexWidget>
  );
}

/** Click action of a toggle segment: the task handler stores the choice and redraws, the app stays closed. */
export const TOGGLE_ACTION = 'SY_TOGGLE_WINDOW';

function Segment({ win, on }: { win: LimitWindow; on: boolean }) {
  return (
    <FlexWidget
      style={{ paddingHorizontal: 7, paddingVertical: 2, borderRadius: 6, backgroundColor: on ? ACCENT : TRACK }}
      clickAction={TOGGLE_ACTION}
      clickActionData={{ window: win }}
      accessibilityLabel={`Show the ${win} window`}
    >
      <TextWidget text={win} style={{ fontSize: 10, fontWeight: 'bold', color: on ? INK : DIM }} />
    </FlexWidget>
  );
}

/** The segmented 5 h | 7 d toggle. */
export function WindowToggle({ selected }: { selected: LimitWindow }) {
  return (
    <FlexWidget style={{ flexDirection: 'row', alignItems: 'center', borderRadius: 7, backgroundColor: TRACK }}>
      <Segment win="5 h" on={selected === '5 h'} />
      <Segment win="7 d" on={selected === '7 d'} />
    </FlexWidget>
  );
}

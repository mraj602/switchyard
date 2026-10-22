// jest-expo renders an @expo/ui/swift-ui widget tree through the
// recording mock, and an Android widget tree through the react-native-android-widget mock.
// The widget component tests use the same pattern: render the pure props → tree component with
// test/render-widget.ts (not RNTL's render, which rejects text outside an RN <Text>), then
// walk the host elements ("SwiftUI.Text", "Android.TextWidget", …).
import { Text, VStack } from '@expo/ui/swift-ui';
import { font } from '@expo/ui/swift-ui/modifiers';
import { FlexWidget, TextWidget } from 'react-native-android-widget';
import { createWidget } from 'expo-widgets';

import { callsOf } from './fakes';
import { allOf, renderWidget, textOf } from './render-widget';

interface Props {
  needsYou: number;
  resetsAt: string;
}

function NeedsYouSmall({ needsYou, resetsAt }: Props) {
  const d = new Date(resetsAt);
  return (
    <VStack spacing={4}>
      <Text modifiers={[font({ size: 28, weight: 'bold' })]}>{String(needsYou)}</Text>
      <Text date={d} dateStyle="timer" timerInterval={{ lower: new Date(0), upper: d }} />
    </VStack>
  );
}

function SyNeedsYou({ needsYou }: Props) {
  return (
    <FlexWidget style={{ height: 'match_parent', width: 'match_parent' }}>
      <TextWidget text={String(needsYou)} />
    </FlexWidget>
  );
}

describe('widget component mocks (fact 4)', () => {
  it('renders a swift-ui tree with a timer text', () => {
    const tree = renderWidget(<NeedsYouSmall needsYou={2} resetsAt="2026-10-13T17:00:00.000Z" />);
    expect(tree.type).toBe('SwiftUI.VStack');
    const texts = allOf(tree, 'SwiftUI.Text');
    expect(texts).toHaveLength(2);
    expect(textOf(texts[0]!)).toBe('2');
    expect(texts[1]!.props.dateStyle).toBe('timer');
    expect((texts[1]!.props.date as Date).toISOString()).toBe('2026-10-13T17:00:00.000Z');
    expect(callsOf('SwiftUI.modifier.font')).toHaveLength(1);
  });

  it('renders an android widget tree', () => {
    const tree = renderWidget(<SyNeedsYou needsYou={3} resetsAt="2026-10-13T17:00:00.000Z" />);
    expect(tree.type).toBe('Android.FlexWidget');
    expect(allOf(tree, 'Android.TextWidget')[0]!.props.text).toBe('3');
  });

  it('records createWidget and updateTimeline', () => {
    const w = createWidget('NeedsYouSmall', NeedsYouSmall as never);
    w.updateTimeline([{ date: new Date(0), props: { needsYou: 1, resetsAt: '2026-10-13T17:00:00.000Z' } }] as never);
    expect(callsOf('widgets.').map((c) => c.fn)).toEqual(['widgets.createWidget', 'widgets.updateTimeline']);
  });
});

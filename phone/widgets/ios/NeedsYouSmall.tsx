// iOS small widget (phone.md P11): the needs-you count, the top item and the next reset as a
// ticking timer. 'widget' components are serialised and evaluated natively with the
// @expo/ui globals, so this one is pure: props in, tree out, no hooks, no module state, no
// import used at run time except the @expo/ui components and modifiers.
import { Text, VStack } from '@expo/ui/swift-ui';
import { font, foregroundStyle, lineLimit, padding, widgetURL } from '@expo/ui/swift-ui/modifiers';

import type { WidgetTimelineProps } from '../../src/lib/widget';

export function NeedsYouSmall(props: WidgetTimelineProps) {
  'widget';
  const s = props.snapshot;
  const dim = foregroundStyle({ type: 'hierarchical', style: 'secondary' });
  if (!s) {
    return (
      <VStack alignment="leading" spacing={4} modifiers={[padding({ all: 12 }), widgetURL('switchyard://now')]}>
        <Text modifiers={[font({ size: 14, weight: 'semibold' })]}>Switchyard</Text>
        <Text modifiers={[font({ size: 12 }), dim]}>Open Switchyard to set up</Text>
      </VStack>
    );
  }
  const top = s.top;
  const next = props.next;
  return (
    <VStack alignment="leading" spacing={2} modifiers={[padding({ all: 12 }), widgetURL('switchyard://now')]}>
      <Text modifiers={[font({ size: 12, weight: 'semibold' }), dim]}>Needs you</Text>
      <Text modifiers={[font({ size: 40, weight: 'bold' }), foregroundStyle(s.needs_you > 0 ? '#ff9f0a' : '#8e8e93')]}>
        {String(s.needs_you)}
      </Text>
      {top ? <Text modifiers={[font({ size: 12 }), lineLimit(1)]}>{top.project + ' · ' + top.what}</Text> : null}
      {next ? (
        <VStack alignment="leading" spacing={0}>
          <Text modifiers={[font({ size: 11 }), dim, lineLimit(1)]}>{next.seat + ' resets in'}</Text>
          <Text date={new Date(next.resets_at)} dateStyle="timer" modifiers={[font({ size: 14, weight: 'semibold' })]} />
        </VStack>
      ) : null}
      {props.stale ? <Text modifiers={[font({ size: 10 }), dim]}>{props.stale}</Text> : null}
    </VStack>
  );
}

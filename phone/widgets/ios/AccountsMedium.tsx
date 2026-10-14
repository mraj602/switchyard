// iOS medium widget (phone.md P11): up to four accounts, each a gauge with its used %, its
// other window and the reset time. Pure 'widget' component (see NeedsYouSmall.tsx).
import { Gauge, HStack, Spacer, Text, VStack } from '@expo/ui/swift-ui';
import { font, foregroundStyle, frame, lineLimit, padding, tint, widgetURL } from '@expo/ui/swift-ui/modifiers';

import type { WidgetTimelineProps } from '../../src/lib/widget';

export function AccountsMedium(props: WidgetTimelineProps) {
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
  return (
    <VStack alignment="leading" spacing={6} modifiers={[padding({ all: 12 }), widgetURL('switchyard://accounts')]}>
      <HStack spacing={6}>
        <Text modifiers={[font({ size: 12, weight: 'semibold' }), dim]}>Accounts</Text>
        <Spacer />
        {props.stale ? <Text modifiers={[font({ size: 10 }), dim]}>{props.stale}</Text> : null}
      </HStack>
      <HStack spacing={10}>
        {props.rows.map((r) => (
          <VStack key={r.id} spacing={3} modifiers={[frame({ maxWidth: 1000 })]}>
            <Text modifiers={[font({ size: 12, weight: 'semibold' }), foregroundStyle(r.colour), lineLimit(1)]}>{r.label}</Text>
            <Gauge value={r.pct / 100} min={0} max={1} modifiers={[tint(r.colour)]} />
            <Text modifiers={[font({ size: 16, weight: 'bold' })]}>{String(r.pct) + '%'}</Text>
            {r.reset ? (
              <Text modifiers={[font({ size: 11 }), dim]}>reset</Text>
            ) : r.resets_at ? (
              <Text date={new Date(r.resets_at)} dateStyle="time" modifiers={[font({ size: 11 }), dim]} />
            ) : null}
            {r.other ? (
              <Text modifiers={[font({ size: 10 }), dim]}>{(r.other.window === 'seven_day' ? '7d ' : '5h ') + String(r.other.pct) + '%'}</Text>
            ) : null}
          </VStack>
        ))}
      </HStack>
    </VStack>
  );
}

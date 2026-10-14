// iOS Live Activity (phone.md P12): a Lock Screen banner and Dynamic Island regions counting
// down to a limit reset with a self-ticking `Text` timerInterval. Pure 'widget' component
// (see NeedsYouSmall.tsx); started and updated by the app only (widgets/ios/adapter.ts).
import { HStack, Spacer, Text, VStack } from '@expo/ui/swift-ui';
import { activityBackgroundTint, font, foregroundStyle, lineLimit, monospacedDigit, padding } from '@expo/ui/swift-ui/modifiers';

import type { LiveCountdownProps } from '../../src/contract/live';

export function ResetCountdown(props: LiveCountdownProps) {
  'widget';
  const range = { lower: new Date(props.started_at), upper: new Date(props.resets_at) };
  const dim = foregroundStyle({ type: 'hierarchical', style: 'secondary' });
  const win = props.window === 'seven_day' ? '7-day' : '5-hour';
  const timer = (size: number) => (
    <Text timerInterval={range} countsDown modifiers={[font({ size, weight: 'bold' }), monospacedDigit(), foregroundStyle(props.colour)]} />
  );
  return {
    banner: (
      <VStack alignment="leading" spacing={4} modifiers={[padding({ all: 14 }), activityBackgroundTint('#1c1b22')]}>
        <HStack spacing={6}>
          <Text modifiers={[font({ size: 13, weight: 'semibold' }), foregroundStyle(props.colour), lineLimit(1)]}>{props.seat}</Text>
          <Text modifiers={[font({ size: 13 }), dim]}>{win + ' limit at ' + String(props.pct) + '%'}</Text>
          <Spacer />
        </HStack>
        {timer(34)}
        <Text modifiers={[font({ size: 12 }), dim]}>until it resets</Text>
      </VStack>
    ),
    compactLeading: <Text modifiers={[font({ size: 12, weight: 'semibold' }), foregroundStyle(props.colour), lineLimit(1)]}>{props.seat}</Text>,
    compactTrailing: timer(13),
    minimal: <Text modifiers={[font({ size: 11, weight: 'bold' }), foregroundStyle(props.colour)]}>{String(props.pct)}</Text>,
    expandedLeading: (
      <VStack alignment="leading" spacing={2}>
        <Text modifiers={[font({ size: 14, weight: 'semibold' }), foregroundStyle(props.colour), lineLimit(1)]}>{props.seat}</Text>
        <Text modifiers={[font({ size: 12 }), dim]}>{win + ' · ' + String(props.pct) + '%'}</Text>
      </VStack>
    ),
    expandedTrailing: timer(22),
    expandedBottom: <Text modifiers={[font({ size: 12 }), dim]}>Tap to open the account.</Text>,
  };
}

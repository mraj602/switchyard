// The Switchyard mark (phone/assets/src/mark.svg, rendered by scripts/gen-icons.sh): the launcher
// icon's gold double chevron, on the app's own screens. Decorative: hidden from screen readers.
import { Image, Text, View } from 'react-native';

const MARK = require('../../assets/mark.png');

export function Mark({ size = 28 }: { size?: number }) {
  return (
    <Image
      source={MARK}
      style={{ width: size, height: size }}
      accessibilityElementsHidden
      importantForAccessibility="no"
      testID="logo-mark"
    />
  );
}

/** The mark with the name next to it (Pair, More). */
export function Logo({ size = 28, colour }: { size?: number; colour: string }) {
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', gap: 10 }}>
      <Mark size={size} />
      <Text style={{ color: colour, fontSize: Math.round(size * 0.75), fontWeight: '700', letterSpacing: 0.2 }}>Switchyard</Text>
    </View>
  );
}

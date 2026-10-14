// Renders a widget component tree for tests (phone.md P11, P12). Widget trees are not React
// Native views, so RNTL's render (which only lets RN <Text> hold strings) does not fit; this
// uses test-renderer directly, with SwiftUI.Text as the text host. The mocks in test/fakes.ts
// turn every @expo/ui/swift-ui and react-native-android-widget component into a host element
// named "SwiftUI.<Name>" / "Android.<Name>".
import { act, type ReactElement } from 'react';
import { createRoot, type TestInstance } from 'test-renderer';

export function renderWidget(element: ReactElement): TestInstance {
  const root = createRoot({ textComponentTypes: ['SwiftUI.Text', 'SwiftUI.Label', 'SwiftUI.Button'] });
  // React's act needs this flag outside RNTL.
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  act(() => root.render(element));
  const top = root.container.children[0];
  if (!top || typeof top === 'string') throw new Error('widget rendered nothing');
  return top;
}

/** All host elements of a type under (and including) node. */
export function allOf(node: TestInstance, type: string): TestInstance[] {
  return [...(node.type === type ? [node] : []), ...node.queryAll((n) => n.type === type)];
}

/** The concatenated text children of a host element. */
export function textOf(node: TestInstance): string {
  return node.children.map((c) => (typeof c === 'string' ? c : textOf(c))).join('');
}

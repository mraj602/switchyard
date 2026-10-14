// Test support: under jest, babel-preset-expo turns every function with the 'widget' directive
// into a STRING of source (the native side evaluates it with the @expo/ui components, the
// modifiers and the JSX runtime as globals). `widgetFn` does what the native runtime does,
// with the recording mocks of test/fakes.ts as those globals, so a test can render the
// component with test/render-widget.ts. In a build where the transform is off the component is
// already a function and is returned as is.
import type { ComponentType } from 'react';

type Fn = (...a: never[]) => unknown;

export function widgetFn<P extends object>(component: unknown): ComponentType<P> {
  if (typeof component === 'function') return component as ComponentType<P>;
  if (typeof component !== 'string') throw new Error('not a widget component');
  const swiftUI = jest.requireMock('@expo/ui/swift-ui') as Record<string, unknown>;
  const modifiers = jest.requireMock('@expo/ui/swift-ui/modifiers') as Record<string, unknown>;
  const runtime = jest.requireActual('react/jsx-runtime') as Record<string, unknown>;
  const scope = new Proxy(
    {},
    {
      has: (_t, k) => typeof k === 'string' && !(k in globalThis),
      get(_t, k) {
        if (typeof k !== 'string') return undefined;
        if (k.startsWith('_jsx')) return runtime[k.slice(1)];
        if (k === '_Fragment') return runtime.Fragment;
        return /^[A-Z]/.test(k) ? swiftUI[k] : modifiers[k];
      },
    },
  );
  // Sloppy-mode `with`, like the global scope of the native evaluator.
  return new Function('scope', `with (scope) { return (${component}); }`)(scope) as Fn as unknown as ComponentType<P>;
}

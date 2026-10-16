// What the Add phone / machine / desktop dialog says when the hub refuses to make a code.
// Pure, so node can test it (pair-error.test.cjs): no imports.
//
// The hub's own sentence is the message (api.js puts the body's `error` in err.message), never
// a generic "HTTP 400". The code `needs_https` (api.yaml makePairCode) gets a heading that
// names the problem: the board was opened over plain http, so a phone has no address to use.
export function pairErrorView(err) {
  const message = (err && err.message) || 'The hub could not make a code.';
  if (err && err.code === 'needs_https') {
    return { code: 'needs_https', title: 'A phone needs the hub’s https address', message };
  }
  return { code: (err && err.code) || '', title: 'Could not make a code', message };
}

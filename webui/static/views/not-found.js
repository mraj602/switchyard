// Any hash route the board does not know: say so, and offer the way back.

import { h } from '../components/dom.js';

export function render() {
  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'Page not found')),
    h('p', { class: 'calm', role: 'status', 'data-key': 'not-found' },
      'There is no page at this address. The link may be old or mistyped. ',
      h('a', { href: '#/', 'data-key': 'not-found-home' }, 'Go to Now')));
  return { title: 'Not found · Watchyard', node };
}

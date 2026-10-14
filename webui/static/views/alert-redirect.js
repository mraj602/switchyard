// #/alerts/<id>: a private push links here. Look the alert up and replace this route with
// its deep link (push.md 5.8).

import { h } from '../components/dom.js';
import { getAlert } from '../api.js';
import { hashOfLink } from '../route.js';

const st = { error: '' };

export async function load({ route }) {
  st.error = '';
  try {
    const a = await getAlert(route.id);
    const target = hashOfLink(a.url);
    // Never loop back to ourselves.
    location.replace(target.startsWith('#/alerts/') ? '#/' : target);
  } catch (e) {
    if (e.name === 'AuthError') return;
    st.error = e.status === 404 ? 'That alert no longer exists.' : e.message;
    window.dispatchEvent(new Event('sy-rerender'));
  }
}

export function render() {
  const node = h('div', { class: 'page' },
    st.error ? h('p', { class: 'calm', role: 'alert' }, st.error, ' ', h('a', { href: '#/' }, 'Go to Now'))
      : h('p', { class: 'muted pad', role: 'status' }, 'Opening…'));
  return { title: 'Switchyard', node };
}

// More (phone tab): Alerts, Machines, Settings, Peers, Analytics; the owner also gets Devices and Add phone.

import { h } from '../components/dom.js';
import { isOwner } from '../store.js';

function item(href, label, sub, extra) {
  return h('li', {}, h('a', { class: 'more-link', href, ...(extra || {}) },
    h('span', { class: 'more-text' }, h('span', { class: 'strong' }, label), sub ? h('span', { class: 'muted small' }, sub) : null),
    h('span', { class: 'chev', 'aria-hidden': 'true' }, '›')));
}

export function render({ store }) {
  const owner = isOwner();
  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'More')),
    h('ul', { class: 'panel more-list' },
      item('#/more/alerts', 'Alerts', 'Which pushes you get, and the recent ones'),
      item('#/machines', 'Machines', `${store.overview.machines.length} reporting to this hub`),
      item('#/more/settings', 'Settings', 'Notifications, this phone, app version'),
      item('#/more/peers', 'Peers', 'Which accounts may message each other, and Pause all peers'),
      owner ? item('#/more/devices', 'Devices', 'Phones, browsers and machines paired with this hub') : null,
      owner ? item('#/more/devices?add=phone', 'Add phone', 'Show a QR code to pair a phone') : null,
      item('#/more/analytics', 'Analytics', 'Tokens and API-equivalent cost by day, model and project')),
    owner ? null : h('p', { class: 'muted small' }, 'Devices and Add phone need an owner sign-in: run sy hub owner-link on the hub.'));
  return { title: 'More · Switchyard', node };
}

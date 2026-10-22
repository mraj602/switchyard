// Quiet hours: the hub-wide setting (owner) or this device's own (push.md 2, 5.2, 5.4).

import { h } from './dom.js';
import { toggle, toast } from './ui.js';
import { tzLabel, localTz, sameTz } from '../format.js';

const DEFAULT = { enabled: false, start: '23:00', end: '08:00' };

// opts: { value: QuietHours|null, hubValue, onSave(qh|null), disabled, device: bool }
// For a device, value null means "follow the hub"; the control shows the hub's setting then.
// The hours are read in the setting's own time zone, so the zone is always shown; when
// the hub has none the browser's zone is assumed and said so.
export function quietHours({ value, hubValue, onSave, disabled, device }) {
  const eff = value || hubValue || DEFAULT;
  const here = localTz();
  const stored = (value && value.tz) || (hubValue && hubValue.tz) || '';
  const tz = stored || here;
  const save = (patch) => onSave({ ...DEFAULT, ...eff, tz, ...patch });
  const time = (key, label) => {
    const input = h('input', { type: 'time', value: eff[key] || DEFAULT[key], 'aria-label': label, 'data-key': `qh-${key}`, disabled: disabled ? true : undefined });
    input.addEventListener('change', () => { if (input.value) save({ [key]: input.value }).catch((e) => toast(e.message)); });
    return input;
  };
  const zone = tz ? `${tzLabel(tz)}${stored ? '' : ' (this device’s zone)'}` : 'time zone unknown';
  const differs = stored && here && !sameTz(stored, here);
  return h('div', { class: 'quiet' },
    toggle({
      label: `Quiet hours · ${eff.start}–${eff.end}`,
      sub: [device ? (value ? 'This device has its own hours' : 'Following the hub') : 'Holds every notification except failures', zone].join(' · '),
      checked: !!eff.enabled, disabled, key: 'qh-enabled',
      onChange: (v) => save({ enabled: v }),
    }),
    eff.enabled ? h('div', { class: 'quiet-times' }, h('label', {}, 'From ', time('start', 'Quiet hours start')), h('label', {}, 'to ', time('end', 'Quiet hours end'))) : null,
    differs && !disabled ? h('p', { class: 'muted small', 'data-key': 'qh-tz' }, `This device is in ${tzLabel(here)}. `,
      h('button', { type: 'button', class: 'linkish', 'data-key': 'qh-use-local', onclick: () => save({ tz: here }).catch((e) => toast(e.message)) }, 'Use this device’s zone')) : null,
    device && value ? h('button', { type: 'button', class: 'linkish', 'data-key': 'qh-follow', disabled: disabled ? true : undefined, onclick: () => onSave(null).catch((e) => toast(e.message)) }, 'Follow the hub instead') : null);
}

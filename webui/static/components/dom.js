// Tiny DOM helper. Everything is set through textContent / setAttribute, never innerHTML,
// so text from the hub (titles, questions, paths) cannot inject markup.

export function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'style' && typeof v === 'object') for (const [p, val] of Object.entries(v)) el.style.setProperty(p, val);
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  append(el, kids);
  return el;
}

export function append(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    el.append(typeof k === 'object' && k.nodeType ? k : document.createTextNode(String(k)));
  }
  return el;
}

// Visually hidden text for screen readers.
export const sr = (text) => h('span', { class: 'sr-only' }, text);

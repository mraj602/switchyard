// The order of the accounts (PUT /v1/seats/order): the pure parts of dragging an account card
// on the Accounts page, kept free of the DOM and of imports so node can test them
// (order.test.cjs).

// seats with the listed ids first, in that order, and the rest after them as they were; each
// seat's `order` becomes its position. Unknown and repeated ids are skipped. This is what the
// hub does with the same list, so the board agrees with it before the answer comes back.
export function applyOrder(seats, ids) {
  const byId = new Map((seats || []).map((s) => [s.id, s]));
  const used = new Set();
  const out = [];
  for (const id of ids || []) {
    if (byId.has(id) && !used.has(id)) { used.add(id); out.push(byId.get(id)); }
  }
  for (const s of seats || []) if (!used.has(s.id)) out.push(s);
  return out.map((s, i) => (s.order === i ? s : { ...s, order: i }));
}

// The ids after moving `id` next to `target`: before it, or after it when `after` is true.
// Returns the same array (not a copy) when nothing would change.
export function moveTo(ids, id, target, after) {
  if (id === target) return ids;
  const from = ids.indexOf(id);
  if (from < 0 || !ids.includes(target)) return ids;
  const rest = ids.filter((x) => x !== id);
  const at = rest.indexOf(target) + (after ? 1 : 0);
  const next = [...rest.slice(0, at), id, ...rest.slice(at)];
  return next.every((x, i) => x === ids[i]) ? ids : next;
}

// The ids after moving `id` up (delta -1) or down (+1) one place; the same array at an end.
export function moveBy(ids, id, delta) {
  const from = ids.indexOf(id);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= ids.length) return ids;
  const next = ids.slice();
  next.splice(from, 1);
  next.splice(to, 0, id);
  return next;
}

// Where a card dropped at the point (x, y) lands, for cards laid out in a wrapping grid.
// rects: [{id, left, top, width, height}] in reading order. The target is the card under the
// point, else the one with the nearest centre; the card goes after it when the point is past
// its middle (to the right when the point is level with the card, else below). Returns
// {target, after}, or null with no cards. The dragged card's own rect counts too, so a
// drop on itself returns itself (a no-op for moveTo).
export function dropTarget(rects, x, y) {
  let best = null;
  let bestD = Infinity;
  for (const r of rects || []) {
    const cx = r.left + r.width / 2;
    const cy = r.top + r.height / 2;
    const inside = x >= r.left && x <= r.left + r.width && y >= r.top && y <= r.top + r.height;
    const d = inside ? -1 : Math.hypot(x - cx, y - cy);
    if (d < bestD) { bestD = d; best = r; }
  }
  if (!best) return null;
  const level = y >= best.top && y <= best.top + best.height;
  const after = level ? x > best.left + best.width / 2 : y > best.top + best.height / 2;
  return { target: best.id, after };
}

// The ids to send for a drop: the shown ids with `id` moved to the drop target; null when the
// order would not change (so nothing is sent).
export function dropOrder(ids, id, drop) {
  if (!drop) return null;
  const next = moveTo(ids, id, drop.target, drop.after);
  return next === ids ? null : next;
}

// What a screen reader hears after a move: "work moved to 2 of 4".
export function moveSentence(name, ids, id) {
  const at = ids.indexOf(id);
  return at < 0 ? '' : `${name} moved to ${at + 1} of ${ids.length}`;
}

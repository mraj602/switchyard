// Work that a reload would throw away (pwa.md 7): an open dialog or modal, or a form the
// person has edited and not submitted. The update flow asks `reason()` before it reloads the
// page by itself and shows the "Update ready" banner while it answers. No imports; node
// tests it with a fake document (pwa.test.cjs).

const MODAL = 'dialog[open], [aria-modal="true"]';

export function createPending(doc) {
  const dirty = new Set(); // forms with an edit since the last submit or reset
  const edit = (e) => {
    const t = e.target;
    // Only fields inside a form count: a search box or a filter is not unsaved work.
    if (t && t.form && !(t.hasAttribute && t.hasAttribute('data-nodirty'))) dirty.add(t.form);
  };
  const clear = (e) => { dirty.delete(e.target && (e.target.form || e.target)); };
  doc.addEventListener('input', edit, true);
  doc.addEventListener('change', edit, true);
  doc.addEventListener('submit', clear, true);
  doc.addEventListener('reset', clear, true);

  // 'modal' | 'unsaved' | null
  function reason() {
    if (doc.querySelector(MODAL)) return 'modal';
    for (const f of [...dirty]) if (!f.isConnected) dirty.delete(f);
    return dirty.size ? 'unsaved' : null;
  }
  return { reason, pending: () => reason() !== null };
}

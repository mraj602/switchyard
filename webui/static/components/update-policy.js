// When the page may switch itself to a waiting build without the person pressing "Reload".
// Pure, so node can test it (pwa.test.cjs): no imports.
//
// A waiting worker keeps a running page on its old files, which is safe but, in an installed
// app that is never closed, leaves the old board on screen for days. So the page applies the
// update by itself at the two moments a reload costs nothing: right after it started (nothing
// has been done in it yet) and while it is in the background. Never while a field has focus,
// and never twice within a minute (a build that cannot take over must not cause a reload loop).

export const FRESH_PAGE_MS = 15e3;
export const AUTO_GAP_MS = 60e3;

export function autoApplyOk({ waiting, editing, hidden, pageAgeMs, sinceLastAutoMs }) {
  if (!waiting || editing) return false;
  if (sinceLastAutoMs != null && sinceLastAutoMs < AUTO_GAP_MS) return false;
  return !!hidden || pageAgeMs < FRESH_PAGE_MS;
}

// Swipe a row sideways on a touch screen: right = first action, left = second. The same
// actions exist as buttons in the row menu, so nothing depends on the gesture.

const THRESHOLD = 88;

export function swipeable(card, { onRight, onLeft }) {
  let x0 = 0, y0 = 0, dx = 0, tracking = false, horizontal = false;

  const move = (x) => { card.style.transform = x ? `translateX(${x}px)` : ''; };

  card.addEventListener('touchstart', (e) => {
    if (e.touches.length !== 1) return;
    x0 = e.touches[0].clientX; y0 = e.touches[0].clientY; dx = 0; tracking = true; horizontal = false;
    card.classList.remove('swipe-settle');
  }, { passive: true });

  card.addEventListener('touchmove', (e) => {
    if (!tracking) return;
    const t = e.touches[0];
    const ddx = t.clientX - x0, ddy = t.clientY - y0;
    if (!horizontal) {
      if (Math.abs(ddy) > 12 && Math.abs(ddy) > Math.abs(ddx)) { tracking = false; return; } // a scroll
      if (Math.abs(ddx) < 10) return;
      horizontal = true;
    }
    dx = Math.max(-140, Math.min(140, ddx));
    card.dataset.swipe = dx > 0 ? 'right' : 'left';
    move(dx);
    if (e.cancelable) e.preventDefault();
  }, { passive: false });

  const end = () => {
    if (!tracking && !horizontal) return;
    tracking = false;
    const done = horizontal ? dx : 0;
    horizontal = false;
    card.classList.add('swipe-settle');
    move(0);
    delete card.dataset.swipe;
    if (done >= THRESHOLD && onRight) onRight();
    else if (done <= -THRESHOLD && onLeft) onLeft();
  };
  card.addEventListener('touchend', end);
  card.addEventListener('touchcancel', end);
}

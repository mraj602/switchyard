// Draws the hub's QR matrix ({size, rows}: rows are strings of 0/1, quiet zone included) as
// SVG rects built with DOM calls. Dark modules are black on a white tile so any phone camera
// reads it in both colour schemes.

import { svg } from './ui.js';

export function qrSvg({ size, rows }, label = 'QR code') {
  const rects = [];
  rows.forEach((row, y) => {
    let x = 0;
    while (x < row.length) {
      if (row[x] !== '1') { x++; continue; }
      let run = 1;
      while (x + run < row.length && row[x + run] === '1') run++;
      rects.push(svg('rect', { x, y, width: run, height: 1 }));
      x += run;
    }
  });
  return svg('svg', { viewBox: `0 0 ${size} ${size}`, class: 'qr', role: 'img', 'aria-label': label, 'shape-rendering': 'crispEdges' },
    svg('rect', { width: size, height: size, fill: '#FFFFFF' }),
    svg('g', { fill: '#000000' }, rects));
}

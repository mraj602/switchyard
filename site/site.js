// Copy buttons for the install commands. Plain text only; no network.
(function () {
  "use strict";
  var buttons = document.querySelectorAll("button.copy[data-copy]");
  Array.prototype.forEach.call(buttons, function (btn) {
    btn.addEventListener("click", function () {
      var src = document.getElementById(btn.getAttribute("data-copy"));
      if (!src) return;
      var text = src.textContent;
      var done = function (ok) {
        btn.textContent = ok ? "Copied" : "Press Ctrl+C";
        setTimeout(function () { btn.textContent = "Copy"; }, 2000);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(false); });
      } else {
        var r = document.createRange();
        r.selectNodeContents(src);
        var s = window.getSelection();
        s.removeAllRanges();
        s.addRange(r);
        done(false);
      }
    });
  });
})();

// The demo video: a visible Pause/Play button (WCAG 2.2.2). The page ships native controls so it
// still works without script; with script they are swapped for the button. Visitors who asked
// for reduced motion get it paused on the poster.
(function () {
  "use strict";
  var v = document.getElementById("demo-media");
  var b = document.getElementById("demo-toggle");
  if (!v || !b) return;
  var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var sync = function () { b.textContent = v.paused ? "Play" : "Pause"; };
  v.removeAttribute("controls");
  b.hidden = false;
  if (reduce) {
    v.removeAttribute("autoplay");
    v.pause();
  }
  b.addEventListener("click", function () {
    if (v.paused) {
      var p = v.play();
      if (p && p.catch) p.catch(function () {});
    } else {
      v.pause();
    }
  });
  v.addEventListener("play", sync);
  v.addEventListener("pause", sync);
  sync();
})();

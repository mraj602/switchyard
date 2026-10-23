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

// The demo video plays on its own unless the visitor asked for reduced motion; then it waits
// on its poster with controls.
(function () {
  "use strict";
  var v = document.getElementById("demo-media");
  if (!v || !window.matchMedia || !window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  v.removeAttribute("autoplay");
  v.pause();
  v.controls = true;
})();

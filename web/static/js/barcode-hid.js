/**
 * barcode-hid.js - HID/USB keystroke barcode scanner support (§8.4d).
 *
 * Hardware scanners emit characters < 60 ms apart and terminate with Enter.
 * This script captures that pattern and fires a custom "barcode:scanned"
 * event, then fills any focused .barcode-field or [data-hid-target] input.
 */
(function () {
  'use strict';

  const GAP_MS = 60;
  const MIN_LEN = 6;

  let buf = '';
  let lastTime = 0;
  let timer = null;

  function flush() {
    if (buf.length >= MIN_LEN) {
      const code = buf;
      // Fill a focused barcode field if present.
      const focused = document.activeElement;
      if (focused && (focused.classList.contains('barcode-field') || focused.dataset.hidTarget)) {
        focused.value = code;
        focused.dispatchEvent(new Event('input', { bubbles: true }));
      } else {
        // Fire global event so other listeners (camera page, etc.) can react.
        document.dispatchEvent(new CustomEvent('barcode:scanned', { detail: { code } }));
      }
    }
    buf = '';
  }

  document.addEventListener('keydown', function (e) {
    // Ignore if inside a normal text field that isn't a barcode target.
    const tag = document.activeElement && document.activeElement.tagName;
    const isBarcodeField = document.activeElement &&
      (document.activeElement.classList.contains('barcode-field') ||
       document.activeElement.dataset.hidTarget);
    if ((tag === 'INPUT' || tag === 'TEXTAREA') && !isBarcodeField) return;

    const now = Date.now();

    if (e.key === 'Enter') {
      clearTimeout(timer);
      flush();
      return;
    }

    // Only single printable characters.
    if (e.key.length !== 1) return;

    if (now - lastTime > GAP_MS && buf.length > 0) {
      // Gap too large - reset (this was human typing, not a scanner burst).
      buf = '';
    }
    lastTime = now;
    buf += e.key;

    // Safety flush after 100 chars with no Enter.
    clearTimeout(timer);
    timer = setTimeout(flush, 200);
  });
})();

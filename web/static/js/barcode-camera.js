/**
 * barcode-camera.js — Camera-based barcode scanning (§8.4d).
 *
 * Uses the native BarcodeDetector API where available (Chrome, Safari 17+).
 * Falls back to QuaggaJS if BarcodeDetector is absent and quagga.min.js is
 * loaded. Fires a custom "barcode:scanned" event with { code } on a confirmed
 * read (two-read confirm threshold to reduce misreads).
 *
 * Expects the scan page DOM:
 *   #scan-video      <video>
 *   #scan-status     status label
 *   #scan-start      start button
 *   #scan-torch      torch toggle button
 *   #scan-corner-svg corner brackets SVG (colour signals state)
 */
(function () {
  'use strict';

  const video = document.getElementById('scan-video');
  const startBtn = document.getElementById('scan-start');
  const torchBtn = document.getElementById('scan-torch');
  const statusEl = document.getElementById('scan-status');
  const cornerSvg = document.getElementById('scan-corner-svg');

  if (!video || !startBtn) return; // not on scan page

  const CONFIRM_READS = 2; // reads required before accepting
  let stream = null;
  let animFrame = null;
  let pending = {}; // code → count
  let track = null;

  function setStatus(msg, color) {
    if (statusEl) statusEl.textContent = msg;
    if (cornerSvg) {
      cornerSvg.querySelectorAll('path').forEach(p => {
        p.setAttribute('stroke', color || 'white');
      });
    }
  }

  function onCode(code) {
    pending[code] = (pending[code] || 0) + 1;
    if (pending[code] >= CONFIRM_READS) {
      pending = {};
      setStatus('✓ ' + code, '#4ade80');
      document.dispatchEvent(new CustomEvent('barcode:scanned', { detail: { code } }));
      // Navigate to /scan/<code> for server-side resolution.
      window.location.href = '/scan/' + encodeURIComponent(code);
    } else {
      setStatus('Detecting…', '#facc15');
    }
  }

  async function startNative() {
    const detector = new BarcodeDetector({ formats: [
      'ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128', 'code_39', 'code_93', 'qr_code'
    ]});

    async function tick() {
      if (video.readyState >= 2) {
        try {
          const codes = await detector.detect(video);
          if (codes.length > 0) onCode(codes[0].rawValue);
          else setStatus('Point at a barcode', 'white');
        } catch (_) { /* ignore decode errors */ }
      }
      animFrame = requestAnimationFrame(tick);
    }
    tick();
  }

  function startQuagga() {
    if (typeof Quagga === 'undefined') {
      setStatus('Camera scanning unavailable — enter code manually.', '#f87171');
      return;
    }
    Quagga.init({
      inputStream: { type: 'LiveStream', target: video.parentElement,
        constraints: { facingMode: 'environment' } },
      decoder: { readers: ['ean_reader', 'ean_8_reader', 'upc_reader', 'code_128_reader'] },
    }, function (err) {
      if (err) { setStatus('Quagga init failed: ' + err, '#f87171'); return; }
      Quagga.start();
    });
    Quagga.onDetected(function (result) {
      onCode(result.codeResult.code);
    });
  }

  async function start() {
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: 'environment', width: { ideal: 1280 } }
      });
      video.srcObject = stream;
      await video.play();

      const tracks = stream.getVideoTracks();
      track = tracks[0] || null;

      // Torch support
      if (track) {
        const caps = track.getCapabilities && track.getCapabilities();
        if (caps && caps.torch) {
          torchBtn.hidden = false;
          let torchOn = false;
          torchBtn.addEventListener('click', async () => {
            torchOn = !torchOn;
            await track.applyConstraints({ advanced: [{ torch: torchOn }] });
            torchBtn.textContent = torchOn ? '🔦 Torch off' : '🔦 Torch';
          });
        }
      }

      setStatus('Point at a barcode', 'white');

      if ('BarcodeDetector' in window) {
        startNative();
      } else {
        startQuagga();
      }
    } catch (err) {
      setStatus('Camera error: ' + err.message, '#f87171');
    }
  }

  startBtn.addEventListener('click', function () {
    startBtn.disabled = true;
    start();
  });

  // Auto-start if arriving here via a scan redirect.
  if (window.location.search.includes('autostart')) start();
})();

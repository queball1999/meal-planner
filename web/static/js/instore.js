/**
 * instore.js - the shopping-mode checklist.
 *
 * Everything here optimises for one situation: standing in an aisle, one hand
 * on a trolley, glancing at a phone. Ticking is instant and local; the server
 * is told afterwards and a failure rolls the row back rather than blocking.
 */

'use strict';

(function () {
    const root = document.getElementById('instore');
    if (!root) return;

    const remainingEl = document.getElementById('instore-remaining');
    const cartEl = document.getElementById('instore-cart');

    function csrf() {
        const f = document.getElementById('csrf-token');
        if (f) return f.value;
        const inForm = document.querySelector('#csrf-form input[name]');
        return inForm ? inForm.value : '';
    }

    function money(text) {
        return parseFloat(String(text).replace(/[^0-9.]/g, '')) || 0;
    }

    function fmt(n) {
        return '$' + n.toFixed(2);
    }

    function recount() {
        let remaining = 0;
        root.querySelectorAll('[data-aisle]').forEach(function (aisle) {
            const left = aisle.querySelectorAll('[data-instore-row]:not(.instore-row--checked)').length;
            const badge = aisle.querySelector('[data-aisle-count]');
            if (badge) badge.textContent = left;
            // A finished aisle collapses rather than disappearing: it still
            // has to be reachable to undo a mis-tap.
            aisle.classList.toggle('instore-aisle--done', left === 0);
            remaining += left;
        });
        if (remainingEl) remainingEl.textContent = remaining;
    }

    function setChecked(row, on) {
        row.classList.toggle('instore-row--checked', on);
        const btn = row.querySelector('.instore-row__btn');
        if (btn) btn.setAttribute('aria-pressed', on ? 'true' : 'false');

        const price = money(row.querySelector('.instore-row__price').textContent);
        if (cartEl) {
            cartEl.textContent = fmt(money(cartEl.textContent) + (on ? price : -price));
        }
        recount();
    }

    root.addEventListener('click', function (e) {
        const row = e.target.closest('[data-instore-row]');
        if (!row) return;

        const on = !row.classList.contains('instore-row--checked');
        // Flip first: at the shelf, the tick has to feel immediate, and a
        // round trip over shop wi-fi is not something to wait on.
        setChecked(row, on);

        fetch('/list/' + row.dataset.id + '/check', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
                'X-CSRF-Token': csrf(),
            },
            body: 'checked=' + (on ? '1' : '0'),
        })
            .then(function (r) {
                if (!r.ok) throw new Error('save failed');
            })
            .catch(function () {
                // Roll back rather than leaving the phone disagreeing with the
                // list the rest of the household can see.
                setChecked(row, !on);
                if (window.showToast) window.showToast('Could not save that - check your signal', 'error');
            });
    });

    // Keep the screen on. A phone that sleeps between aisles means unlocking
    // it with one hand at every shelf.
    //
    // Best-effort by design: Wake Lock needs a secure context and is missing
    // on several browsers, and this is a convenience - a page that refused to
    // work without it would be worse than one that occasionally dims.
    let lock = null;
    async function acquireLock() {
        if (!('wakeLock' in navigator)) return;
        try {
            lock = await navigator.wakeLock.request('screen');
        } catch (_) {
            lock = null;
        }
    }
    acquireLock();

    // The lock is dropped whenever the tab is hidden, so it has to be taken
    // again on return - otherwise it survives exactly one glance away.
    document.addEventListener('visibilitychange', function () {
        if (document.visibilityState === 'visible' && lock === null) acquireLock();
    });
})();

/* Pantry page: change a row's quantity (-/+ or typing) and unit in place.
 * Each change posts to /pantry/{id}/update; a failed save puts the old value
 * back and says why. */
(function () {
    'use strict';

    // g and ml are bought in hundreds, so a step of 1 is no use; everything
    // else counts in ones.
    var STEPS = { g: 50, ml: 50 };

    function csrf() {
        var f = document.getElementById('csrf-token');
        return f ? f.value : '';
    }

    function fmt(n) {
        return String(Math.round(n * 100) / 100);
    }

    function fail(msg) {
        if (window.goeat && window.goeat.alert) window.goeat.alert(msg, { title: 'Pantry' });
    }

    function save(id, fields, onOk, onFail) {
        var body = new URLSearchParams();
        Object.keys(fields).forEach(function (k) { body.set(k, fields[k]); });
        fetch('/pantry/' + id + '/update', {
            method: 'POST',
            headers: { 'X-CSRF-Token': csrf(), 'Accept': 'application/json' },
            body: body,
        })
            .then(function (r) {
                return r.json().catch(function () { return { ok: false }; });
            })
            .then(function (d) {
                if (d && d.ok) { onOk(d); return; }
                onFail((d && d.error) || "Couldn't save that change.");
            })
            .catch(function () { onFail("Couldn't reach the server."); });
    }

    function rowOf(el) {
        return el.closest('tr');
    }

    var timers = new WeakMap();

    function commitQty(input) {
        var stepper = input.closest('[data-pantry-row]');
        var id = stepper.getAttribute('data-pantry-row');
        var v = parseFloat(input.value);
        if (isNaN(v) || v < 0) { input.value = input.dataset.saved; return; }
        if (String(v) === input.dataset.saved) return;
        stepper.classList.add('is-saving');
        save(id, { quantity: v }, function (d) {
            input.value = fmt(d.quantity);
            input.dataset.saved = input.value;
            stepper.classList.remove('is-saving');
        }, function (msg) {
            input.value = input.dataset.saved;
            stepper.classList.remove('is-saving');
            fail(msg);
        });
    }

    document.querySelectorAll('[data-qty-input]').forEach(function (input) {
        input.dataset.saved = input.value;
    });

    document.addEventListener('click', function (e) {
        if (!(e.target instanceof Element)) return;
        var btn = e.target.closest('[data-qty-dec], [data-qty-inc]');
        if (!btn) return;
        var stepper = btn.closest('[data-pantry-row]');
        var input = stepper.querySelector('[data-qty-input]');
        var unitSel = rowOf(btn).querySelector('[data-unit-select]');
        var step = STEPS[unitSel ? unitSel.value : ''] || 1;
        var cur = parseFloat(input.value) || 0;
        var next = btn.hasAttribute('data-qty-inc') ? cur + step : Math.max(0, cur - step);
        input.value = fmt(next);
        // Rapid taps collapse into one save.
        clearTimeout(timers.get(input));
        timers.set(input, setTimeout(function () { commitQty(input); }, 450));
    });

    document.addEventListener('change', function (e) {
        var t = e.target;
        if (!(t instanceof Element)) return;
        if (t.matches('[data-qty-input]')) {
            clearTimeout(timers.get(t));
            commitQty(t);
        } else if (t.matches('[data-unit-select]')) {
            var id = rowOf(t).querySelector('[data-pantry-row]').getAttribute('data-pantry-row');
            var prev = t.dataset.saved || t.defaultValue;
            var chosen = t.value;
            save(id, { unit: chosen }, function (d) {
                t.dataset.saved = d.unit;
            }, function (msg) {
                t.value = prev;
                fail(msg);
            });
        }
    });

    document.querySelectorAll('[data-unit-select]').forEach(function (sel) {
        sel.dataset.saved = sel.value;
    });
})();

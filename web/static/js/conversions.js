/**
 * conversions.js - quick unit-conversion popup for the shopping list's
 * ruler/swap icon (one per line, next to the price-edit pencil).
 *
 * Same dual-mode model as hovercard.js, deliberately: a mouse hovering the
 * button previews the popup and it closes the moment the pointer leaves; a
 * click (mouse or touch - a tap fires click either way) pins it open until a
 * second click on the same button, Escape, or a click elsewhere. The `pinned`
 * flag is what keeps the two modes from fighting each other - every
 * hover-driven open/close bails out immediately while pinned, so a tap that
 * also fires a synthetic mouseover/mouseout on some mobile browsers can never
 * undo what the click just did.
 *
 * One floating popup for the whole list, moved to whatever button is active,
 * `position: fixed` and placed at show time - the shopping list, like every
 * scrolling table in this app, clips an absolutely positioned descendant
 * regardless of z-index (see popover.js and hovercard.js for the same note).
 */

'use strict';

window.goeat = window.goeat || {};

(function () {
    const OPEN_DELAY = 300;
    const CLOSE_DELAY = 150;

    let popup = null;
    let openTimer = null;
    let closeTimer = null;
    let currentBtn = null;
    let pinned = false;
    const cache = new Map();

    function build() {
        if (popup) return popup;
        popup = document.createElement('div');
        popup.className = 'popover conv-popup';
        popup.setAttribute('role', 'tooltip');
        popup.hidden = true;
        // Keeping the pointer inside the popup holds it open on a hover
        // preview, same as hovercard.js, so a reader can move onto it.
        popup.addEventListener('mouseenter', function () { clearTimeout(closeTimer); });
        popup.addEventListener('mouseleave', function () { if (!pinned) scheduleHide(); });
        document.body.appendChild(popup);
        return popup;
    }

    function render(data) {
        const p = build();
        p.innerHTML = '';

        const rows = (data && data.conversions) || [];
        if (!rows.length) {
            const empty = document.createElement('div');
            empty.className = 'conv-popup__empty';
            empty.textContent = data && data.qty
                ? 'No other units make sense for ' + data.qty + '.'
                : 'No conversions available.';
            p.appendChild(empty);
            return;
        }

        const list = document.createElement('ul');
        list.className = 'conv-popup__list';
        rows.forEach(function (row) {
            const li = document.createElement('li');
            li.className = 'conv-popup__row';
            // textContent: labels are built server-side from plain numbers and
            // unit names, but never trust formatted strings into innerHTML.
            li.textContent = row.label;
            list.appendChild(li);
        });
        p.appendChild(list);
    }

    // Mirrors popover.js's place(): measure with the popup actually rendered
    // (display:none while `.popover` lacks its open state would otherwise
    // read a zero-size box), then clamp into the viewport on both axes so the
    // popup never hangs off the right edge of a narrow phone or the bottom of
    // a short window.
    function position(btn) {
        const r = btn.getBoundingClientRect();
        const gap = 8;
        const p = build();

        p.hidden = false;
        p.style.visibility = 'hidden';
        p.style.display = 'block';
        const pw = p.offsetWidth;
        const ph = p.offsetHeight;
        const vw = document.documentElement.clientWidth;
        const vh = document.documentElement.clientHeight;

        let left = r.right - pw; // right-align under the button, like a menu
        left = Math.max(gap, Math.min(left, vw - pw - gap));

        let top = r.bottom + gap;
        if (top + ph > vh - gap) top = r.top - ph - gap; // flip above if no room below
        top = Math.max(gap, top);

        p.style.position = 'fixed';
        p.style.top = Math.round(top) + 'px';
        p.style.left = Math.round(left) + 'px';
        p.style.visibility = '';
        p.style.display = '';
    }

    function show(btn) {
        const id = btn.dataset.id;
        if (!id) return;
        currentBtn = btn;

        function done(data) {
            // The pointer (or focus) may have moved to a different button
            // while the fetch was in flight; rendering now would pop this
            // button's popup over whatever is hovered next. A pinned open was
            // deliberate (a click), so it always renders.
            if (!pinned && currentBtn !== btn) return;
            render(data);
            const p = build();
            p.hidden = false;
            p.classList.add('popover--open');
            btn.setAttribute('aria-expanded', 'true');
            position(btn);
        }

        if (cache.has(id)) { done(cache.get(id)); return; }

        fetch('/list/' + id + '/conversions', { headers: { Accept: 'application/json' } })
            .then(function (r) { return r.json(); })
            .then(function (d) { cache.set(id, d); done(d); })
            .catch(function () { /* a popup with nothing to show is not worth an error */ });
    }

    function hide() {
        if (currentBtn) currentBtn.setAttribute('aria-expanded', 'false');
        currentBtn = null;
        pinned = false;
        if (popup) { popup.classList.remove('popover--open'); popup.hidden = true; }
    }

    function scheduleHide() {
        clearTimeout(openTimer);
        clearTimeout(closeTimer);
        closeTimer = setTimeout(function () { if (!pinned) hide(); }, CLOSE_DELAY);
    }

    document.addEventListener('mouseover', function (e) {
        if (pinned) return;
        if (!(e.target instanceof Element)) return;
        const btn = e.target.closest('[data-conv-btn]');
        if (!btn || btn === currentBtn) return;
        clearTimeout(closeTimer);
        clearTimeout(openTimer);
        openTimer = setTimeout(function () { show(btn); }, OPEN_DELAY);
    });

    document.addEventListener('mouseout', function (e) {
        if (pinned) return;
        if (!(e.target instanceof Element)) return;
        if (!e.target.closest('[data-conv-btn]')) return;
        scheduleHide();
    });

    // A click (mouse or a tap's synthesized click) pins the popup open
    // instead of relying on hover - the only mode a touch device gets.
    document.addEventListener('click', function (e) {
        if (!(e.target instanceof Element)) return;
        if (popup && popup.contains(e.target)) return;

        const btn = e.target.closest('[data-conv-btn]');
        if (btn) {
            e.preventDefault();
            clearTimeout(openTimer);
            clearTimeout(closeTimer);
            if (pinned && currentBtn === btn) { hide(); return; }
            pinned = true;
            show(btn);
            return;
        }

        if (pinned) hide();
    });

    // A fixed popup cannot follow its trigger. A hover preview closes on
    // scroll; a pinned one is nudged back into place instead - same rule
    // hovercard.js and popover.js both use for the same containers.
    function onScrollResize() {
        if (!popup || !popup.classList.contains('popover--open')) return;
        if (pinned && currentBtn) { position(currentBtn); return; }
        hide();
    }
    window.addEventListener('scroll', onScrollResize, true);
    window.addEventListener('resize', onScrollResize);
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') hide();
    });
})();

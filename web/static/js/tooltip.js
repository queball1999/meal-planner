/**
 * tooltip.js - hover-and-click help popups for every `.tooltip-btn` (the "(?)"
 * widget beside a field, card title, or table header).
 *
 * Hovering previews the popup and it closes the moment the pointer leaves;
 * clicking locks it open until its own close "x", a second click on the same
 * button, or a click anywhere else on the page. Text comes from
 * `data-tooltip` (falling back to `title`, so a button that hasn't been
 * updated yet still shows something) and the native `title` is removed so the
 * browser's own tooltip never fights with this one.
 *
 * `.tooltip-popup` is `position: fixed`, positioned by positionPopup() at show
 * time - not `position: absolute` under `.tooltip-wrap`. An absolutely
 * positioned descendant is clipped by its nearest scrolling ancestor's padding
 * box regardless of z-index, and this app's tables sit inside
 * `overflow-x: auto` wrappers, so a tooltip button in a table header would be
 * cut off with no z-index able to rescue it. The cost is that a fixed element
 * cannot follow its trigger, so every popup closes on scroll and resize.
 *
 * goeat.enhanceTooltips(root) is exported so content inserted after page load
 * (modal bodies, fetched fragments) can be wired too; it is safe to call
 * repeatedly.
 */

'use strict';

window.goeat = window.goeat || {};

(function () {
    function closeAll(except) {
        document.querySelectorAll('.tooltip-popup.open').forEach(function (popup) {
            if (popup === except) return;
            hide(popup);
        });
    }

    function hide(popup) {
        popup.classList.remove('open');
        popup._locked = false;
        if (popup._btn) popup._btn.classList.remove('tooltip-open');
    }

    // Centered under the trigger and clamped to the viewport. Always below:
    // the arrow only ever points up, and a help popup is short enough that a
    // bottom-of-viewport trigger is rare enough not to earn a flip branch.
    // The popup must already be `.open` (display: block) so it can be measured.
    function positionPopup(btn, popup) {
        const rect = btn.getBoundingClientRect();
        const gap = 8;

        popup.style.left = '0px';
        const width = popup.getBoundingClientRect().width;

        let left = rect.left + rect.width / 2 - width / 2;
        left = Math.min(left, window.innerWidth - width - gap);
        left = Math.max(gap, left);

        popup.style.left = Math.round(left) + 'px';
        popup.style.top = Math.round(rect.bottom + gap) + 'px';
    }

    function show(popup, locked) {
        closeAll(popup);
        popup.classList.add('open');
        positionPopup(popup._btn, popup);
        popup._locked = !!locked;
        if (popup._btn) popup._btn.classList.add('tooltip-open');
    }

    function buildPopup(btn) {
        const text = btn.getAttribute('data-tooltip') || btn.getAttribute('title') || '';
        if (!text) return null;
        btn.removeAttribute('title');

        const wrap = document.createElement('span');
        wrap.className = 'tooltip-wrap';
        btn.parentNode.insertBefore(wrap, btn);
        wrap.appendChild(btn);

        const popup = document.createElement('div');
        popup.className = 'tooltip-popup';
        popup.setAttribute('role', 'tooltip');
        popup._btn = btn;

        const body = document.createElement('div');
        body.className = 'tooltip-popup-body';
        body.textContent = text;

        const close = document.createElement('button');
        close.type = 'button';
        close.className = 'tooltip-popup-close';
        close.setAttribute('aria-label', 'Close');
        close.textContent = '×';
        close.addEventListener('click', function (e) {
            e.stopPropagation();
            hide(popup);
        });

        popup.appendChild(close);
        popup.appendChild(body);
        popup.addEventListener('click', function (e) { e.stopPropagation(); });
        wrap.appendChild(popup);
        return popup;
    }

    /**
     * Wires every not-yet-enhanced `.tooltip-btn` under root. Safe to call
     * repeatedly, including over content that mixes already-enhanced and fresh
     * buttons - each is built at most once (`data-tooltip-wired`).
     * @param {ParentNode} root
     */
    function enhanceTooltips(root) {
        (root || document).querySelectorAll('.tooltip-btn').forEach(function (btn) {
            if (btn.hasAttribute('data-tooltip-wired')) return;
            const popup = buildPopup(btn);
            if (!popup) return;
            btn.setAttribute('data-tooltip-wired', '');

            btn.addEventListener('mouseenter', function () {
                if (popup._locked) return;
                show(popup, false);
            });
            btn.addEventListener('mouseleave', function () {
                if (popup._locked) return;
                hide(popup);
            });
            btn.addEventListener('click', function (e) {
                e.preventDefault();
                e.stopPropagation();
                if (popup.classList.contains('open')) {
                    hide(popup);
                } else {
                    show(popup, true);
                }
            });
        });
    }

    document.addEventListener('DOMContentLoaded', function () { enhanceTooltips(document); });
    document.addEventListener('click', function () { closeAll(); });
    window.addEventListener('scroll', function () { closeAll(); }, true);
    window.addEventListener('resize', function () { closeAll(); });

    window.goeat.enhanceTooltips = enhanceTooltips;
})();

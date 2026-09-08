/**
 * popover.js - one floating panel primitive for the whole app.
 *
 * A popover is a panel that opens from a trigger, floats above page content
 * (fixed position, its own stacking context), and closes on outside-click,
 * Escape, a second press of the trigger, or - opt-in - activating something
 * inside it. The mobile nav menu and the mobile search both ride on this so
 * there is one set of open/close/focus/dismiss rules, not three.
 *
 * Declarative wiring, no per-page JS:
 *
 *   <button data-popover="siteMenu" aria-expanded="false">Menu</button>
 *   <div class="popover" id="siteMenu" data-popover-panel hidden> ... </div>
 *
 * Optional attributes on the trigger:
 *   data-popover-placement="bottom-end" | "bottom-start" | "bottom" (default bottom-end)
 *   data-popover-dismiss="tap"   close as soon as a link/button inside is activated
 *   data-popover-focus="input"   CSS selector, inside the panel, to focus on open
 *
 * Imperative escape hatch: goeat.popover.open(id) / .close(id) / .toggle(id).
 *
 * Loaded as a classic script after modal.js so it can extend window.goeat.
 */

'use strict';

window.goeat = window.goeat || {};

(function () {
    const OPEN_CLASS = 'popover--open';
    const open = new Map(); // id -> { trigger, panel, onDocClick, onKeydown, prevFocus }

    function panelFor(id) {
        const el = document.getElementById(id);
        return el && el.hasAttribute('data-popover-panel') ? el : null;
    }

    function firstFocusable(panel, sel) {
        if (sel) {
            const pick = panel.querySelector(sel);
            if (pick) return pick;
        }
        return panel.querySelector(
            'a[href], button:not([disabled]), input:not([disabled]), ' +
            'select:not([disabled]), textarea:not([disabled]), [tabindex="0"]'
        );
    }

    function place(trigger, panel) {
        const placement = trigger.getAttribute('data-popover-placement') || 'bottom-end';
        const r = trigger.getBoundingClientRect();
        const gap = 8;

        // Measure with the panel actually rendered: `.popover:not(.popover--open)`
        // is display:none, and that class is only added after place() returns,
        // so offsetWidth/offsetHeight would read as 0 without this override -
        // every placement branch below would then be computed against a
        // zero-size panel. visibility:hidden keeps it invisible during the
        // measurement without collapsing it.
        panel.hidden = false;
        panel.style.visibility = 'hidden';
        panel.style.display = 'block';
        const pw = panel.offsetWidth;
        const ph = panel.offsetHeight;
        const vw = document.documentElement.clientWidth;
        const vh = document.documentElement.clientHeight;

        let left;
        if (placement === 'bottom-start') left = r.left;
        else if (placement === 'bottom') left = r.left + r.width / 2 - pw / 2;
        else left = r.right - pw; // bottom-end

        left = Math.max(gap, Math.min(left, vw - pw - gap));

        let top = r.bottom + gap;
        top = Math.max(gap, Math.min(top, vh - ph - gap));

        panel.style.position = 'fixed';
        panel.style.top = Math.round(top) + 'px';
        panel.style.left = Math.round(left) + 'px';
        panel.style.visibility = '';
        panel.style.display = '';
    }

    function openPopover(id) {
        if (open.has(id)) return;
        const panel = panelFor(id);
        const trigger = document.querySelector('[data-popover="' + CSS.escape(id) + '"]');
        if (!panel || !trigger) return;

        // Only one popover at a time - opening a second closes the first.
        open.forEach(function (_v, otherID) { if (otherID !== id) closePopover(otherID); });

        const prevFocus = document.activeElement;
        place(trigger, panel);
        panel.classList.add(OPEN_CLASS);
        trigger.setAttribute('aria-expanded', 'true');

        const target = firstFocusable(panel, trigger.getAttribute('data-popover-focus'));
        if (target) target.focus();

        function onDocClick(e) {
            if (panel.contains(e.target) || trigger.contains(e.target)) {
                if (trigger.getAttribute('data-popover-dismiss') === 'tap' &&
                    e.target.closest('a[href], button')) {
                    // let the activation happen, then close
                    setTimeout(function () { closePopover(id); }, 0);
                }
                return;
            }
            closePopover(id);
        }
        function onKeydown(e) {
            if (e.key === 'Escape') {
                e.preventDefault();
                closePopover(id);
                trigger.focus();
            }
        }
        // Capture so a stopPropagation() inside the panel cannot trap us open.
        document.addEventListener('click', onDocClick, true);
        document.addEventListener('keydown', onKeydown, true);
        window.addEventListener('resize', reposition, true);
        window.addEventListener('scroll', reposition, true);

        function reposition() { if (open.has(id)) place(trigger, panel); }

        open.set(id, { trigger: trigger, panel: panel, onDocClick: onDocClick, onKeydown: onKeydown, reposition: reposition, prevFocus: prevFocus });
    }

    function closePopover(id) {
        const st = open.get(id);
        if (!st) return;
        open.delete(id);

        st.panel.classList.remove(OPEN_CLASS);
        st.panel.hidden = true;
        st.panel.style.position = '';
        st.panel.style.top = '';
        st.panel.style.left = '';
        st.trigger.setAttribute('aria-expanded', 'false');

        document.removeEventListener('click', st.onDocClick, true);
        document.removeEventListener('keydown', st.onKeydown, true);
        window.removeEventListener('resize', st.reposition, true);
        window.removeEventListener('scroll', st.reposition, true);

        if (st.prevFocus instanceof HTMLElement && document.body.contains(st.prevFocus)) {
            st.prevFocus.focus();
        }
    }

    function togglePopover(id) {
        if (open.has(id)) closePopover(id); else openPopover(id);
    }

    // Delegated trigger binding: covers triggers added after load, and is
    // idempotent however many times a page wires itself.
    document.addEventListener('click', function (e) {
        const trigger = e.target instanceof Element && e.target.closest('[data-popover]');
        if (!trigger) return;
        e.preventDefault();
        togglePopover(trigger.getAttribute('data-popover'));
    });

    goeat.popover = { open: openPopover, close: closePopover, toggle: togglePopover };
})();

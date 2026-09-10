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
 *   data-popover-restore         reopen this popover after a page load, so a
 *                                form inside it can navigate without the panel
 *                                disappearing (see "Restore across a reload")
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

    // A panel shorter than this is not worth flipping/scrolling into - below
    // it we just take whichever side has more room and let it scroll.
    const MIN_PANEL_H = 120;

    function place(trigger, panel) {
        const placement = trigger.getAttribute('data-popover-placement') || 'bottom-end';
        const r = trigger.getBoundingClientRect();
        const gap = 8;

        // Measure with the panel actually rendered: `.popover:not(.popover--open)`
        // is display:none, and that class is only added after place() returns,
        // so offsetWidth/offsetHeight would read as 0 without this override -
        // every placement branch below would then be computed against a
        // zero-size panel. visibility:hidden keeps it invisible during the
        // measurement without collapsing it. maxHeight is cleared first so a
        // cap left by a previous open doesn't shrink this measurement.
        panel.hidden = false;
        panel.style.maxHeight = '';
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

        // Vertical: sit under the trigger when it fits, flip above when it
        // doesn't, and only then fall back to the roomier side with the panel
        // capped so it scrolls internally.
        //
        // This used to be a single clamp - `min(r.bottom + gap, vh - ph - gap)`
        // - which for a tall panel (.popover allows up to 70vh) slid it up the
        // viewport until it no longer touched its trigger at all: the day
        // headcount picker on /plan opened pinned near the top of the screen,
        // nowhere near the button that opened it.
        const spaceBelow = vh - r.bottom - gap * 2;
        const spaceAbove = r.top - gap * 2;
        const below = ph <= spaceBelow || spaceBelow >= spaceAbove;

        let top, avail;
        if (below) {
            top = r.bottom + gap;
            avail = spaceBelow;
        } else {
            avail = spaceAbove;
            top = Math.max(gap, r.top - Math.min(ph, avail) - gap);
        }

        panel.style.maxHeight = Math.max(MIN_PANEL_H, avail) + 'px';
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
        rememberRestore(id, trigger);
        announce(panel, 'open', id);
    }

    function closePopover(id) {
        const st = open.get(id);
        if (!st) return;
        open.delete(id);
        forgetRestore(id);

        st.panel.classList.remove(OPEN_CLASS);
        st.panel.hidden = true;
        st.panel.style.position = '';
        st.panel.style.top = '';
        st.panel.style.left = '';
        st.panel.style.maxHeight = '';
        st.trigger.setAttribute('aria-expanded', 'false');

        document.removeEventListener('click', st.onDocClick, true);
        document.removeEventListener('keydown', st.onKeydown, true);
        window.removeEventListener('resize', st.reposition, true);
        window.removeEventListener('scroll', st.reposition, true);

        if (st.prevFocus instanceof HTMLElement && document.body.contains(st.prevFocus)) {
            st.prevFocus.focus();
        }

        announce(st.panel, 'close', id);
    }

    // Bubbling `goeat:popover-open` / `goeat:popover-close` on the panel, so a
    // form wrapped around one can tell whether the user is still working in it.
    // main.js's data-autosubmit handler uses this to hold its save while the
    // panel is open and fire the moment it closes.
    function announce(panel, kind, id) {
        panel.dispatchEvent(new CustomEvent('goeat:popover-' + kind, {
            bubbles: true,
            detail: { id: id },
        }));
    }

    function togglePopover(id) {
        if (open.has(id)) closePopover(id); else openPopover(id);
    }

    // ── Restore across a reload ──────────────────────────────────────────────
    // A form inside a popover navigates when it saves - the plan day's people
    // picker autosubmits, the server rescales the day and re-renders - which
    // would drop the panel mid-task. Adding a guest and then choosing which
    // meals that guest eats is one intent spanning that reload, so a trigger
    // marked data-popover-restore reopens itself on the next load.
    const RESTORE_KEY = 'goeat:popover-restore';

    function rememberRestore(id, trigger) {
        if (!trigger.hasAttribute('data-popover-restore')) return;
        try {
            sessionStorage.setItem(RESTORE_KEY, JSON.stringify({ id: id, path: location.pathname }));
        } catch (_) {}
    }

    function forgetRestore(id) {
        try {
            const raw = sessionStorage.getItem(RESTORE_KEY);
            if (raw && JSON.parse(raw).id === id) sessionStorage.removeItem(RESTORE_KEY);
        } catch (_) {}
    }

    function restoreOpen() {
        let saved;
        try {
            const raw = sessionStorage.getItem(RESTORE_KEY);
            if (!raw) return;
            // One shot: reopening re-arms it through rememberRestore, so a run
            // of guest edits each keeps the panel up, while closing it - or
            // wandering off to another page - does not resurrect it later.
            sessionStorage.removeItem(RESTORE_KEY);
            saved = JSON.parse(raw);
        } catch (_) { return; }

        // Same page only: the id is per-day, and a stale entry should not pop a
        // panel open on some unrelated screen.
        if (!saved || saved.path !== location.pathname || !panelFor(saved.id)) return;
        openPopover(saved.id);

        // Placement is measured from the trigger's rect; if images or fonts are
        // still settling that rect moves, so re-place once everything has loaded.
        if (document.readyState !== 'complete') {
            window.addEventListener('load', function once() {
                window.removeEventListener('load', once);
                const st = open.get(saved.id);
                if (st) place(st.trigger, st.panel);
            });
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', restoreOpen);
    } else {
        restoreOpen();
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

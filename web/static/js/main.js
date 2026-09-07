/**
 * main.js - global JS for Go Eat.
 * Dark mode toggle, hamburger nav, toast notifications, autosave.
 */

'use strict';

// ── MDI icon markup (pictogrammers.com/library/mdi) ───────────────────────

const ICONS = {
    sun:  '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M3.55,19.09L4.96,20.5L6.76,18.71L5.34,17.29M12,6C8.69,6 6,8.69 6,12S8.69,18 12,18 18,15.31 18,12C18,8.68 15.31,6 12,6M20,13H23V11H20M17.24,18.71L19.04,20.5L20.45,19.09L18.66,17.29M20.45,5L19.04,3.6L17.24,5.39L18.66,6.81M13,1H11V4H13M6.76,5.39L4.96,3.6L3.55,5L5.34,6.81L6.76,5.39M1,13H4V11H1M13,20H11V23H13Z"/></svg>',
    moon: '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M17.75,4.09L15.22,6.03L16.13,9.09L13.5,7.28L10.87,9.09L11.78,6.03L9.25,4.09L12.44,4L13.5,1L14.56,4L17.75,4.09M21.25,11L19.61,12.25L20.2,14.23L18.5,13.06L16.8,14.23L17.39,12.25L15.75,11L17.81,10.95L18.5,9L19.19,10.95L21.25,11M18.97,15.95C19.8,15.87 20.69,17.05 20.16,17.8C19.84,18.25 19.5,18.67 19.08,19.07C15.17,23 8.84,23 4.94,19.07C1.03,15.17 1.03,8.83 4.94,4.93C5.34,4.53 5.76,4.17 6.21,3.85C6.96,3.32 8.14,4.21 8.06,5.04C7.79,7.9 8.75,10.87 10.95,13.06C13.14,15.26 16.1,16.22 18.97,15.95M17.33,17.97C14.5,17.81 11.7,16.64 9.53,14.5C7.36,12.31 6.2,9.5 6.04,6.68C3.23,9.82 3.34,14.64 6.35,17.66C9.37,20.67 14.19,20.78 17.33,17.97Z"/></svg>',
    check: '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M21,7L9,19L3.5,13.5L4.91,12.09L9,16.17L19.59,5.59L21,7Z"/></svg>',
    close: '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M19,6.41L17.59,5L12,10.59L6.41,5L5,6.41L10.59,12L5,17.59L6.41,19L12,13.41L17.59,19L19,17.59L13.41,12L19,6.41Z"/></svg>',
    information: '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M13,9H11V7H13M13,17H11V11H13M12,2A10,10 0 0,0 2,12A10,10 0 0,0 12,22A10,10 0 0,0 22,12A10,10 0 0,0 12,2Z"/></svg>',
    alert: '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M13,13H11V7H13M13,17H11V15H13M12,2A10,10 0 0,0 2,12A10,10 0 0,0 12,22A10,10 0 0,0 22,12A10,10 0 0,0 12,2Z"/></svg>',
    eye: '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12,9A3,3 0 0,1 15,12A3,3 0 0,1 12,15A3,3 0 0,1 9,12A3,3 0 0,1 12,9M12,4.5C17,4.5 21.27,7.61 23,12C21.27,16.39 17,19.5 12,19.5C7,19.5 2.73,16.39 1,12C2.73,7.61 7,4.5 12,4.5M3.18,12C4.83,15.36 8.24,17.5 12,17.5C15.76,17.5 19.17,15.36 20.82,12C19.17,8.64 15.76,6.5 12,6.5C8.24,6.5 4.83,8.64 3.18,12Z"/></svg>',
    'eye-off': '<svg class="icon" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M2,5.27L3.28,4L20,20.72L18.73,22L15.65,18.92C14.5,19.3 13.28,19.5 12,19.5C7,19.5 2.73,16.39 1,12C1.69,10.24 2.79,8.69 4.19,7.46L2,5.27M12,9A3,3 0 0,1 15,12C15,12.35 14.94,12.69 14.83,13L11,9.17C11.31,9.06 11.65,9 12,9M12,4.5C17,4.5 21.27,7.61 23,12C22.18,14.08 20.79,15.88 19,17.19L17.58,15.76C18.94,14.82 20.06,13.54 20.82,12C19.17,8.64 15.76,6.5 12,6.5C10.91,6.5 9.84,6.68 8.84,7L7.3,5.47C8.74,4.85 10.33,4.5 12,4.5M3.18,12C4.83,15.36 8.24,17.5 12,17.5C12.69,17.5 13.37,17.43 14,17.29L11.72,15C10.29,14.85 9.15,13.71 9,12.28L5.6,8.87C4.61,9.72 3.78,10.78 3.18,12Z"/></svg>',
};

// ── Dark mode toggle ─────────────────────────────────────────────────────

(function initTheme() {
    const root = document.documentElement;
    const btn  = document.getElementById('themeToggle');
    const icon = btn && btn.querySelector('.theme-icon');

    const STORED = localStorage.getItem('theme'); // 'light' | 'dark' | null

    function applyTheme(theme) {
        root.dataset.theme = theme;
        if (icon) icon.innerHTML = theme === 'dark' ? ICONS.sun : ICONS.moon;
        try { localStorage.setItem('theme', theme); } catch (_) {}
    }

    if (STORED === 'dark' || STORED === 'light') {
        applyTheme(STORED);
    } else {
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        if (icon) icon.innerHTML = prefersDark ? ICONS.sun : ICONS.moon;
    }

    if (btn) {
        btn.addEventListener('click', function () {
            const current = root.dataset.theme;
            const next = current === 'dark' ? 'light' : 'dark';
            applyTheme(next);
        });
    }
})();

// ── Hamburger nav toggle ──────────────────────────────────────────────────

(function initNav() {
    const toggle = document.getElementById('navToggle');
    if (!toggle) return;

    toggle.addEventListener('click', function () {
        const open = document.body.classList.toggle('nav-open');
        toggle.setAttribute('aria-expanded', String(open));
    });

    const nav = document.getElementById('site-nav');
    if (nav) {
        nav.addEventListener('click', function (e) {
            if (e.target.closest('a')) {
                document.body.classList.remove('nav-open');
                toggle.setAttribute('aria-expanded', 'false');
            }
        });
    }

    document.addEventListener('click', function (e) {
        if (!toggle.contains(e.target) && !(nav && nav.contains(e.target))) {
            document.body.classList.remove('nav-open');
            toggle.setAttribute('aria-expanded', 'false');
        }
    });
})();

// ── Toast notification system ─────────────────────────────────────────────
// Universal module: window.showToast(message, type, duration) covers every
// status this app raises (autosave results, AI test connection, settings
// validation errors, ...). type is 'success' | 'info' | 'warning' | 'error',
// default 'info'; duration is ms before auto-dismiss, 0 = stays until closed.

const TOAST_ICONS = {
    success: ICONS.check,
    info:    ICONS.information,
    warning: ICONS.alert,
    error:   ICONS.close,
};

(function initToast() {
    let container = null;
    function getContainer() {
        if (!container) {
            container = document.getElementById('toast-container');
        }
        if (!container) {
            container = document.createElement('div');
            container.id = 'toast-container';
            container.setAttribute('role', 'region');
            container.setAttribute('aria-live', 'polite');
            container.setAttribute('aria-atomic', 'true');
            document.body.appendChild(container);
        }
        return container;
    }

    function showToast(msg, type, duration) {
        type = (type && TOAST_ICONS[type]) ? type : 'info';
        duration = duration === undefined ? 4000 : duration;

        const t = document.createElement('div');
        t.className = 'toast toast--' + type;
        t.setAttribute('role', 'status');
        t.innerHTML = '<span class="toast__icon">' + TOAST_ICONS[type] + '</span>' +
                      '<span class="toast__msg"></span>' +
                      '<button class="toast__close" type="button" aria-label="Dismiss notification">' + ICONS.close + '</button>';
        // textContent, never innerHTML: toast messages routinely carry
        // server-supplied strings (validation errors, fetch failures).
        t.querySelector('.toast__msg').textContent = String(msg);

        const closeButton = t.querySelector('.toast__close');

        getContainer().appendChild(t);
        requestAnimationFrame(function () { t.classList.add('toast--visible'); });

        // Guarded: click-anywhere, the close button, and the auto-dismiss
        // timer can all fire for the same toast - without this a second
        // call re-arms transitionend on an element already mid-removal.
        let dismissed = false;
        function dismiss() {
            if (dismissed) return;
            dismissed = true;
            t.classList.remove('toast--visible');
            t.addEventListener('transitionend', function () { t.remove(); }, { once: true });
        }

        t.addEventListener('click', dismiss);
        closeButton.addEventListener('click', function (e) {
            e.stopPropagation();
            dismiss();
        });

        if (duration > 0) {
            const timer = setTimeout(dismiss, duration);
            t.addEventListener('mouseenter', function () { clearTimeout(timer); });
        }

        return { dismiss: dismiss };
    }

    window.showToast    = showToast;
    window.toastSuccess = function (msg, duration) { return showToast(msg, 'success', duration); };
    window.toastInfo    = function (msg, duration) { return showToast(msg, 'info', duration); };
    window.toastWarning = function (msg, duration) { return showToast(msg, 'warning', duration); };
    window.toastError   = function (msg, duration) { return showToast(msg, 'error', duration); };

    // Server-rendered notifications (redirect-after-POST results) arrive as
    // window.__notify from layout.html and funnel through the same toasts as
    // client-side ones, so there's a single notification system, not two.
    // Notify kind uses "danger" (server-side naming); toasts use "error" -
    // map it. Danger/warning stay until dismissed since they often carry an
    // action ("Check Settings -> AI Logs...").
    if (window.__notify && window.__notify.msg) {
        var kind = window.__notify.kind === 'danger' ? 'error' : (window.__notify.kind || 'info');
        var duration = (kind === 'error' || kind === 'warning') ? 0 : 4000;
        showToast(window.__notify.msg, kind, duration);
    }
})();

// ── Password show/hide toggle ─────────────────────────────────────────────
// Wraps every password field with a visibility toggle button. Every password
// field in this app is present in its page's initial HTML, so a single pass
// at DOMContentLoaded covers all of them.

function wirePasswordToggle(input) {
    if (!input || input.closest('.password-field-wrap')) return;

    // Best-effort opt-out hints for password managers with an inline icon of
    // their own (Bitwarden, LastPass, 1Password, Dashlane); not universal.
    input.setAttribute('data-lpignore', 'true');
    input.setAttribute('data-1p-ignore', 'true');
    input.setAttribute('data-bwignore', 'true');
    input.setAttribute('data-form-type', 'other');

    const wrapper = document.createElement('span');
    wrapper.className = 'password-field-wrap';
    input.parentNode.insertBefore(wrapper, input);
    wrapper.appendChild(input);

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'password-toggle';
    button.setAttribute('aria-label', 'Show password');
    button.innerHTML = ICONS.eye;
    wrapper.appendChild(button);

    function toggle() {
        const show = input.type === 'password';
        input.type = show ? 'text' : 'password';
        button.innerHTML = show ? ICONS['eye-off'] : ICONS.eye;
        button.setAttribute('aria-label', show ? 'Hide password' : 'Show password');
    }

    button.addEventListener('click', function () {
        toggle();
        input.focus();
    });
}

document.addEventListener('DOMContentLoaded', function () {
    document.querySelectorAll('input[type="password"]').forEach(wirePasswordToggle);
});

// Ctrl+Space toggles whichever password field currently has focus.
document.addEventListener('keydown', function (e) {
    if (!e.ctrlKey || e.code !== 'Space') return;
    const wrapper = document.activeElement && document.activeElement.closest('.password-field-wrap');
    if (!wrapper) return;
    const input = wrapper.querySelector('input');
    const button = wrapper.querySelector('.password-toggle');
    if (!input || !button) return;
    e.preventDefault();
    button.click();
});

// ── CSRF token helper (shared by autosave and fetch calls) ────────────────

function goeatCSRF() {
    const f = document.getElementById('csrf-token');
    return f ? f.value : '';
}

// ── Autosave with debounce ────────────────────────────────────────────────
// Forms with data-autosave="true" are saved on any change event after 800ms.
// The form action endpoint must accept a POST and return 2xx or a redirect.

(function initAutosave() {
    document.querySelectorAll('form[data-autosave]').forEach(function (form) {
        let timer;
        let saving = false;

        function save() {
            if (saving) return;
            saving = true;

            const fd = new FormData(form);
            const url = form.getAttribute('action') || window.location.pathname;

            // Attach CSRF token both in FormData (if not already present via hidden input)
            // and as a header so the middleware accepts it.
            const token = goeatCSRF();

            fetch(url, {
                method: 'POST',
                body: fd,
                headers: token ? { 'X-CSRF-Token': token } : {},
                redirect: 'manual' // don't follow server redirects - success is any 2xx or 3xx
            })
            .then(function (r) {
                // 2xx = ok; 0 = opaque redirect (fetch redirect:manual); both mean saved.
                if (r.ok || r.status === 0 || (r.status >= 300 && r.status < 400)) {
                    window.showToast && window.showToast('Saved', 'success');
                } else {
                    window.showToast && window.showToast('Save failed (' + r.status + ')', 'error');
                }
            })
            .catch(function (err) {
                window.showToast && window.showToast('Save failed', 'error');
            })
            .finally(function () { saving = false; });
        }

        form.addEventListener('change', function () {
            clearTimeout(timer);
            timer = setTimeout(save, 800);
        });

        // Submit button still works normally - just prevent double-save.
        form.addEventListener('submit', function () {
            clearTimeout(timer);
        });
    });
})();

// ── Choices.js enhancement ───────────────────────────────────────────────
// Any <select data-choices> becomes a searchable dropdown. Multi-selects
// (<select data-choices multiple>) get removable tags. Loaded from the
// vendored choices.min.js (see layout.html); no-ops if that failed to load.

(function initChoices() {
    if (typeof window.Choices === 'undefined') return;
    var selects = document.querySelectorAll('select[data-choices]');
    for (var i = 0; i < selects.length; i++) {
        var el = selects[i];
        if (el.dataset.choicesReady) continue;
        el.dataset.choicesReady = '1';
        new window.Choices(el, {
            searchEnabled: true,
            shouldSort: false,
            itemSelectText: '',
            removeItemButton: el.multiple,
            allowHTML: false,
        });
    }
})();

// ── Auto-filter ───────────────────────────────────────────────────────────
// A GET form marked data-autofilter submits itself instead of waiting for a
// Filter button: text fields on a debounce, everything discrete (select,
// date, checkbox, radio) the moment it changes. Every list page's Filter
// button is gone, so this is the only way those filters are applied.
//
// Two problems come with submitting a *navigation* while the user is typing,
// and both are handled here rather than per page:
//
//   1. The new document arrives with focus on <body>, so the next keystroke
//      goes nowhere. The field name and caret position are stashed in
//      sessionStorage keyed by pathname and restored on load.
//   2. The old rows sit there looking live for the whole round trip. If the
//      form names a data-skeleton target, its contents are swapped for
//      shimmer rows the instant the request starts.

(function initAutoFilter() {
    const DEBOUNCE_MS = 450;
    const FOCUS_KEY = 'goeat:autofilter:focus';

    function isTextual(el) {
        if (el.tagName === 'TEXTAREA') return true;
        if (el.tagName !== 'INPUT') return false;
        return ['text', 'search', 'number', 'tel', 'url', 'email'].indexOf(el.type) !== -1;
    }

    // Fills the target with shimmer placeholders. A <tbody> needs real rows
    // with the right column count or the table collapses mid-request, which
    // looks worse than showing nothing; anything else gets plain bars.
    function paintSkeleton(target) {
        if (!target) return;
        const rows = Math.min(Math.max(target.children.length, 3), 8);
        if (target.tagName === 'TBODY') {
            const table = target.closest('table');
            const head = table && table.querySelector('thead tr');
            const cols = head ? head.children.length : 1;
            let html = '';
            for (let i = 0; i < rows; i++) {
                html += '<tr aria-hidden="true"><td colspan="' + cols + '">' +
                        '<div class="skeleton-row"></div></td></tr>';
            }
            target.innerHTML = html;
            return;
        }
        let html = '';
        for (let i = 0; i < rows; i++) {
            html += '<div class="skeleton-row" aria-hidden="true" style="margin:var(--sp-3) 0"></div>';
        }
        target.innerHTML = html;
    }

    function rememberFocus(field) {
        if (!field || !field.name) return;
        const state = { path: window.location.pathname, name: field.name, pos: null };
        // selectionStart throws on input types that have no text selection
        // (date, checkbox, ...), which is exactly the set where there is no
        // caret worth restoring.
        try { state.pos = field.selectionStart; } catch (_) {}
        try { sessionStorage.setItem(FOCUS_KEY, JSON.stringify(state)); } catch (_) {}
    }

    function restoreFocus() {
        let state;
        try {
            const raw = sessionStorage.getItem(FOCUS_KEY);
            if (!raw) return;
            sessionStorage.removeItem(FOCUS_KEY);
            state = JSON.parse(raw);
        } catch (_) { return; }
        if (!state || state.path !== window.location.pathname) return;

        const field = document.querySelector('[data-autofilter] [name="' + CSS.escape(state.name) + '"]');
        if (!field) return;
        field.focus();
        if (state.pos !== null && state.pos !== undefined) {
            try { field.setSelectionRange(state.pos, state.pos); } catch (_) {}
        }
    }

    document.querySelectorAll('form[data-autofilter]').forEach(function (form) {
        let timer;

        function go(field) {
            clearTimeout(timer);
            rememberFocus(field);
            const sel = form.getAttribute('data-skeleton');
            if (sel) paintSkeleton(document.querySelector(sel));
            form.submit();
        }

        form.addEventListener('input', function (e) {
            const el = e.target;
            if (!isTextual(el)) return;
            clearTimeout(timer);
            timer = setTimeout(function () { go(el); }, DEBOUNCE_MS);
        });

        form.addEventListener('change', function (e) {
            const el = e.target;
            if (isTextual(el)) return; // its own debounce owns it
            go(el);
        });

        // Enter in a text field applies immediately rather than waiting out
        // the debounce - the form has no submit button left to press.
        form.addEventListener('keydown', function (e) {
            if (e.key !== 'Enter') return;
            if (!isTextual(e.target)) return;
            e.preventDefault();
            go(e.target);
        });
    });

    restoreFocus();
})();

// ── Auto-submit ───────────────────────────────────────────────────────────
// A form marked data-autosubmit saves itself on a debounce instead of waiting
// for a Save button, and navigates to the server's response the way a normal
// submit would.
//
// Distinct from data-autosave above, which posts in the background and shows a
// toast: this is for a form whose result changes the page around it - the plan
// day's people picker rescales that day's recipes and rebuilds the shopping
// list, so the page has to come back rendered from the new state.
//
// Distinct from data-autofilter too: that is a GET navigation that restores the
// caret afterwards; this is a POST whose response is a redirect.

(function initAutoSubmit() {
    const DEBOUNCE_MS = 700;

    document.querySelectorAll('form[data-autosubmit]').forEach(function (form) {
        let timer;
        form.addEventListener('change', function () {
            clearTimeout(timer);
            // Longer than the filter debounce on purpose: ticking three people
            // off a list is one intent, and firing a rescale between each tick
            // would be three round trips and two wrong shopping lists.
            timer = setTimeout(function () { form.submit(); }, DEBOUNCE_MS);
        });
    });
})();

// ── Item quick-add ────────────────────────────────────────────────────────
// One dialog for the page, retargeted per row: a catalog can run to hundreds
// of items, and one overlay each would be hundreds of overlays in the DOM for
// a control most of them never open.
//
// The two footer buttons are both submits; whichever was clicked sets the
// destination. Using two buttons rather than a radio pair means the choice and
// the commit are one gesture instead of two.

(function initQuickAdd() {
    const form = document.getElementById('quick-add-form');
    if (!form) return;

    const dest = document.getElementById('quick-add-destination');
    const qty = document.getElementById('quick-add-qty');
    const price = document.getElementById('quick-add-price');
    const nameEl = document.getElementById('quick-add-name');
    const unitEl = document.getElementById('quick-add-unit');

    document.querySelectorAll('[data-quick-add]').forEach(function (btn) {
        btn.addEventListener('click', function () {
            form.action = '/pantry/items/' + btn.dataset.id + '/quick-add';
            // textContent: item names are user-supplied.
            nameEl.textContent = btn.dataset.name;
            unitEl.textContent = 'Measured in ' + (btn.dataset.unit || 'each') + '.';
            qty.value = btn.dataset.qty || '1';
            price.value = '';
            window.goeat.openModal('quick-add');
        });
    });

    form.querySelectorAll('[data-quick-dest]').forEach(function (btn) {
        btn.addEventListener('click', function () {
            dest.value = btn.dataset.quickDest;
        });
    });
})();

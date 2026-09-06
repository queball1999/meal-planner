/**
 * main.js — global JS for Go Eat.
 * Dark mode toggle, hamburger nav, toast notifications, autosave.
 */

'use strict';

// ── Dark mode toggle ─────────────────────────────────────────────────────

(function initTheme() {
    const root = document.documentElement;
    const btn  = document.getElementById('themeToggle');
    const icon = btn && btn.querySelector('.theme-icon');

    const STORED = localStorage.getItem('theme'); // 'light' | 'dark' | null

    function applyTheme(theme) {
        root.dataset.theme = theme;
        if (icon) icon.textContent = theme === 'dark' ? '☀️' : '🌙';
        try { localStorage.setItem('theme', theme); } catch (_) {}
    }

    if (STORED === 'dark' || STORED === 'light') {
        applyTheme(STORED);
    } else {
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        if (icon) icon.textContent = prefersDark ? '☀️' : '🌙';
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

(function initToast() {
    let container = document.getElementById('toast-container');
    if (!container) {
        container = document.createElement('div');
        container.id = 'toast-container';
        container.setAttribute('aria-live', 'polite');
        container.setAttribute('aria-atomic', 'false');
        document.body.appendChild(container);
    }

    function showToast(msg, type) {
        type = type || 'success';
        const t = document.createElement('div');
        t.className = 'toast toast--' + type;
        t.setAttribute('role', 'status');

        const icon = { success: '✓', error: '✕', info: 'ℹ' }[type] || '';
        t.innerHTML = '<span class="toast__icon">' + icon + '</span>' +
                      '<span class="toast__msg">' + String(msg).replace(/</g,'&lt;') + '</span>' +
                      '<button class="toast__close" aria-label="Dismiss">×</button>';

        t.querySelector('.toast__close').addEventListener('click', function () {
            dismiss(t);
        });

        container.appendChild(t);
        // Force reflow so the transition fires.
        void t.offsetHeight;
        t.classList.add('toast--visible');

        const timer = setTimeout(function () { dismiss(t); }, 4000);
        t.addEventListener('mouseenter', function () { clearTimeout(timer); });

        return t;
    }

    function dismiss(t) {
        t.classList.remove('toast--visible');
        t.addEventListener('transitionend', function () { t.remove(); }, { once: true });
    }

    window.showToast = showToast;
})();

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
                redirect: 'manual' // don't follow server redirects — success is any 2xx or 3xx
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

        // Submit button still works normally — just prevent double-save.
        form.addEventListener('submit', function () {
            clearTimeout(timer);
        });
    });
})();

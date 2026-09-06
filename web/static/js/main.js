/**
 * main.js — minimal JS island for Phase 0.
 * Handles dark-mode toggle. All future islands are additional files or
 * functions added here; no framework, no bundler.
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

    // On load: honour stored preference, else leave data-theme="" (system).
    if (STORED === 'dark' || STORED === 'light') {
        applyTheme(STORED);
    } else {
        // Reflect the system preference in the icon without locking data-theme.
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        if (icon) icon.textContent = prefersDark ? '☀️' : '🌙';
    }

    if (btn) {
        btn.addEventListener('click', function () {
            const current = root.dataset.theme;
            // Cycle: '' (system) → explicit opposite → other explicit.
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

    // Close nav when a link is activated (single-page-style navigation).
    const nav = document.getElementById('site-nav');
    if (nav) {
        nav.addEventListener('click', function (e) {
            if (e.target.closest('a')) {
                document.body.classList.remove('nav-open');
                toggle.setAttribute('aria-expanded', 'false');
            }
        });
    }

    // Close nav on outside click.
    document.addEventListener('click', function (e) {
        if (!toggle.contains(e.target) && !(nav && nav.contains(e.target))) {
            document.body.classList.remove('nav-open');
            toggle.setAttribute('aria-expanded', 'false');
        }
    });
})();

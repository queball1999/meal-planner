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

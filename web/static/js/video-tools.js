/* Video recipe import tools (phase 16).
 *
 * Two places use this:
 *   - Settings → AI Setup ([data-video-tools]): the full setup
 *     walkthrough - one row per tool with Download / Update / Remove, the
 *     speech model picker, and Verify.
 *   - Import Recipe's "limited" banner ([data-video-banner]): one
 *     "Download now" button that installs whatever is missing, shows
 *     progress, and reloads once the tools check out.
 *
 * Both read the same JSON (video.Snapshot) from /settings/video-tools, so
 * they always agree on what is installed. Admin-only endpoints: the banner
 * only renders its button for admins. */
(function () {
    'use strict';

    var TOOLS = {
        'yt-dlp':  { name: 'yt-dlp', purpose: 'Reads the post: caption, creator comments and the audio track. Required.' },
        'ffmpeg':  { name: 'FFmpeg', purpose: 'Converts the audio so it can be transcribed. Required.' },
        'whisper': { name: 'whisper.cpp', purpose: 'Speech-to-text, running on this machine. Without it only the caption is read.' },
        'model':   { name: 'Speech model', purpose: 'The Whisper model whisper.cpp listens with. Pick one below.' }
    };

    function csrf() {
        var f = document.getElementById('csrf-token');
        return f ? f.value : '';
    }

    function esc(s) {
        return String(s == null ? '' : s)
            .replace(/&/g, '&amp;').replace(/</g, '&lt;')
            .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }

    function mb(n) {
        if (!n) return '';
        return n >= 1048576 ? Math.round(n / 1048576) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';
    }

    function post(url, fields) {
        var body = new URLSearchParams();
        Object.keys(fields || {}).forEach(function (k) {
            [].concat(fields[k]).forEach(function (v) { body.append(k, v); });
        });
        return fetch(url, {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'X-CSRF-Token': csrf(), 'Content-Type': 'application/x-www-form-urlencoded' },
            body: body
        }).then(function (r) {
            return r.json().catch(function () { return {}; }).then(function (j) {
                if (!r.ok) throw new Error(j.error || ('Request failed: ' + r.status));
                return j;
            });
        });
    }

    function status() {
        return fetch('/settings/video-tools', { credentials: 'same-origin' }).then(function (r) {
            if (!r.ok) throw new Error('Could not load tool status (' + r.status + ')');
            return r.json();
        });
    }

    /* Calls onSnap with every snapshot until no install is running. */
    function watch(onSnap, onError) {
        function tick() {
            status().then(function (s) {
                onSnap(s);
                if (s.running) setTimeout(tick, 700);
            }).catch(onError || function () {});
        }
        tick();
    }

    function progressText(id, p) {
        var name = TOOLS[id] ? TOOLS[id].name : id;
        switch (p.state) {
        case 'queued':      return name + ': waiting…';
        case 'downloading':
            if (!p.total) return name + ': looking up the latest version…';
            return name + ': downloading ' + mb(p.done) + ' of ' + mb(p.total);
        case 'installing':  return name + ': unpacking…';
        case 'done':        return name + ': installed' + (p.version ? ' (' + p.version + ')' : '');
        case 'failed':      return name + ': failed - ' + p.error;
        }
        return name;
    }

    function bar(p) {
        var pct = p && p.total ? Math.min(100, Math.round(p.done * 100 / p.total)) : 0;
        return '<div class="vt-bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + pct + '">' +
               '<div class="vt-bar__fill" style="width:' + pct + '%"></div></div>';
    }

    /* The one line that sums the whole install up, for the banner. */
    function overall(s) {
        var active = s.tools.filter(function (t) { return t.progress && t.progress.state !== 'done' && t.progress.state !== 'failed' && t.progress.state !== 'queued'; })[0];
        if (active) return { text: progressText(active.id, active.progress), p: active.progress };
        if (s.running) return { text: 'Checking the tools work…', p: null };
        return null;
    }

    /* ── Settings card ───────────────────────────────────────────────── */
    function initSettings(root) {
        var rowsEl   = root.querySelector('[data-vt-rows]');
        var modelsEl = root.querySelector('[data-vt-models]');
        var badge    = root.querySelector('[data-vt-badge]');
        var msg      = root.querySelector('[data-vt-msg]');
        var allBtn   = root.querySelector('[data-vt-install-missing]');
        var verifyBtn = root.querySelector('[data-vt-verify]');
        var checkedEl = root.querySelector('[data-vt-checked]');
        var running = false;

        function say(text, kind) {
            msg.textContent = text || '';
            msg.className = 'field-hint' + (kind ? ' vt-msg--' + kind : '');
        }

        function checkLine(t) {
            if (!t.check) return '';
            return '<div class="vt-check vt-check--' + (t.check.ok ? 'ok' : 'bad') + '">' +
                   (t.check.ok ? '&#10003; Works: ' : '&#10007; ') + esc(t.check.detail) + '</div>';
        }

        function render(s) {
            running = s.running;
            var labels = { full: ['Ready', 'success'], caption: ['Captions only', 'warning'], none: ['Not set up', 'muted'] };
            var l = labels[s.capability] || labels.none;
            badge.className = 'badge badge-' + l[1];
            badge.textContent = l[0];

            rowsEl.innerHTML = s.tools.filter(function (t) { return t.id !== 'model'; }).map(function (t) {
                var info = TOOLS[t.id];
                var state;
                if (t.installed) {
                    state = '<span class="badge badge-success">Installed</span>';
                } else {
                    state = '<span class="badge ' + (t.id === 'whisper' ? 'badge-warning' : 'badge-danger') + '">Missing</span>';
                }
                var where = '';
                if (t.installed && t.source === 'downloaded') where = 'Downloaded' + (t.version ? ' - version ' + esc(t.version) : '');
                if (t.installed && t.source === 'system') where = 'Using the copy installed on this system: <code>' + esc(t.path) + '</code>';

                var actions = '';
                if (!t.installed && t.can_install) {
                    actions = '<button type="button" class="btn btn-primary btn-sm" data-vt-install="' + t.id + '">Download (' + mb(t.size) + ')</button>';
                } else if (t.installed && t.source === 'downloaded') {
                    actions = '<button type="button" class="btn btn-ghost btn-sm" data-vt-install="' + t.id + '">' + (t.id === 'yt-dlp' ? 'Update' : 'Reinstall') + '</button>' +
                              '<button type="button" class="btn btn-ghost btn-sm" data-vt-remove="' + t.id + '">Remove</button>';
                } else if (!t.installed) {
                    actions = '<span class="field-hint">No download for this system - install it with your package manager.</span>';
                }

                var prog = '';
                if (t.progress && t.progress.state !== 'done') {
                    prog = '<div class="field-hint">' + esc(progressText(t.id, t.progress)) + '</div>' +
                           (t.progress.state === 'failed' ? '' : bar(t.progress));
                }
                return '<div class="vt-row">' +
                         '<div class="vt-row__main">' +
                           '<div class="vt-row__name">' + esc(info.name) + ' ' + state + '</div>' +
                           '<div class="field-hint">' + esc(info.purpose) + '</div>' +
                           (where ? '<div class="field-hint">' + where + '</div>' : '') +
                           prog + checkLine(t) +
                         '</div>' +
                         '<div class="vt-row__actions">' + actions + '</div>' +
                       '</div>';
            }).join('');

            var modelTool = s.tools.filter(function (t) { return t.id === 'model'; })[0] || {};
            var mp = modelTool.progress;
            modelsEl.innerHTML = s.models.map(function (m) {
                var actions;
                if (m.active) {
                    actions = '<span class="badge badge-success">In use</span>' +
                              '<button type="button" class="btn btn-ghost btn-sm" data-vt-remove-model="' + m.name + '">Remove</button>';
                } else if (m.installed) {
                    actions = '<button type="button" class="btn btn-ghost btn-sm" data-vt-use-model="' + m.name + '">Use this</button>' +
                              '<button type="button" class="btn btn-ghost btn-sm" data-vt-remove-model="' + m.name + '">Remove</button>';
                } else {
                    actions = '<button type="button" class="btn btn-' + (m.name === 'base.en' ? 'primary' : 'ghost') + ' btn-sm" data-vt-install="model" data-model="' + m.name + '">Download (' + mb(m.size) + ')</button>';
                }
                return '<div class="vt-row' + (m.active ? ' vt-row--active' : '') + '">' +
                         '<div class="vt-row__main">' +
                           '<div class="vt-row__name">' + esc(m.label) + '</div>' +
                           '<div class="field-hint">' + esc(m.note) + '</div>' +
                         '</div>' +
                         '<div class="vt-row__actions">' + actions + '</div>' +
                       '</div>';
            }).join('') +
            (mp && mp.state !== 'done' ? '<div class="field-hint">' + esc(progressText('model', mp)) + '</div>' + (mp.state === 'failed' ? '' : bar(mp)) : '') +
            checkLine(modelTool);

            var anyMissing = s.missing && s.missing.length;
            allBtn.hidden = !anyMissing;
            allBtn.disabled = running;
            verifyBtn.disabled = running;
            root.querySelectorAll('[data-vt-install],[data-vt-remove],[data-vt-use-model],[data-vt-remove-model]').forEach(function (b) {
                b.disabled = running;
            });
            checkedEl.textContent = s.checked_at
                ? 'Last checked ' + new Date(s.checked_at).toLocaleTimeString([], { timeZone: document.body.dataset.appTz || undefined })
                : 'Not checked yet - press Verify.';
            if (running) say('Working… you can leave this page; the download keeps going.');
        }

        function act(promise, doneText) {
            say('Working…');
            promise.then(function (s) {
                render(s);
                if (s.running) {
                    watch(function (snap) {
                        render(snap);
                        if (!snap.running) say(snap.capability === 'full' ? 'Done - every tool works.' : 'Done. Check the rows above for anything that still needs attention.', snap.capability === 'full' ? 'ok' : 'warn');
                    });
                } else {
                    say(doneText || '', 'ok');
                }
            }).catch(function (e) { say(e.message, 'bad'); });
        }

        root.addEventListener('click', function (e) {
            var b = e.target.closest('button');
            if (!b || !root.contains(b)) return;
            if (b.hasAttribute('data-vt-install')) {
                act(post('/settings/video-tools/install', { component: b.dataset.vtInstall, model: b.dataset.model || '' }));
            } else if (b.hasAttribute('data-vt-remove')) {
                act(post('/settings/video-tools/remove', { component: b.dataset.vtRemove }), 'Removed.');
            } else if (b.hasAttribute('data-vt-use-model')) {
                act(post('/settings/video-tools/model', { name: b.dataset.vtUseModel }), 'Now using ' + b.dataset.vtUseModel + '.');
            } else if (b.hasAttribute('data-vt-remove-model')) {
                act(post('/settings/video-tools/remove', { model: b.dataset.vtRemoveModel }), 'Removed.');
            } else if (b === allBtn) {
                act(post('/settings/video-tools/install', { missing: '1', model: 'base.en' }));
            } else if (b === verifyBtn) {
                say('Running each tool…');
                verifyBtn.disabled = true;
                post('/settings/video-tools/verify', {}).then(function (s) {
                    render(s);
                    say(s.capability === 'full' && s.tools.every(function (t) { return t.check && t.check.ok; })
                        ? 'Every tool works.' : 'Some tools need attention - see the rows above.',
                        s.capability === 'full' ? 'ok' : 'warn');
                }).catch(function (e) { say(e.message, 'bad'); verifyBtn.disabled = false; });
            }
        });

        watch(render, function (e) { say(e.message, 'bad'); });
    }

    /* ── Import Recipe banner ────────────────────────────────────────── */
    function initBanner(banner) {
        var btn  = banner.querySelector('[data-video-download]');
        var out  = banner.querySelector('[data-video-progress]');
        if (!btn || !out) return;

        function show(s) {
            var o = overall(s);
            if (o) {
                out.innerHTML = esc(o.text) + (o.p ? bar(o.p) : '');
                return;
            }
            // Finished.
            var failed = s.tools.filter(function (t) {
                return (t.progress && t.progress.state === 'failed') || (t.check && !t.check.ok && t.installed);
            });
            if (s.capability === 'full' && !failed.length) {
                out.textContent = 'All set - video import is ready. Reloading…';
                setTimeout(function () { window.location.reload(); }, 1200);
                return;
            }
            btn.disabled = false;
            out.innerHTML = 'Some tools couldn’t be set up: ' + failed.map(function (t) {
                return esc((TOOLS[t.id] || {}).name || t.id) + ' (' + esc(t.progress && t.progress.error || (t.check && t.check.detail)) + ')';
            }).join('; ') + '. <a href="/settings/ai#video-tools">Open Settings → AI Setup</a> for details.';
        }

        function start() {
            btn.disabled = true;
            out.hidden = false;
            out.textContent = 'Starting…';
            post('/settings/video-tools/install', { missing: '1', model: 'base.en' })
                .then(function () { watch(show); })
                .catch(function (e) {
                    if (/already running/.test(e.message)) { watch(show); return; }
                    btn.disabled = false;
                    out.textContent = e.message;
                });
        }

        btn.addEventListener('click', start);
        if (banner.hasAttribute('data-video-running')) {
            btn.disabled = true;
            out.hidden = false;
            watch(show);
        }
    }

    function init() {
        document.querySelectorAll('[data-video-tools]').forEach(initSettings);
        document.querySelectorAll('[data-video-banner]').forEach(initBanner);
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
    else init();
})();

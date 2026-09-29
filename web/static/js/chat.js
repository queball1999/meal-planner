/**
 * chat.js - the site-wide assistant panel.
 *
 * The assistant makes real changes to the household's plan, so two things
 * matter more than the chat feel: every tool call it makes is shown as it
 * happens, and the page is reloaded (panel reopened) as soon as a turn that
 * changed something finishes - otherwise the user is looking at a plan that
 * no longer matches the database and does not know it.
 */

'use strict';

(function () {
    const launcher = document.getElementById('chat-launcher');
    const panel = document.getElementById('chat-panel');
    if (!launcher || !panel) return;

    const log = document.getElementById('chat-log');
    const form = document.getElementById('chat-form');
    const input = document.getElementById('chat-input');
    const sendBtn = document.getElementById('chat-send');
    const clearBtn = document.getElementById('chat-clear');
    const closeBtn = document.getElementById('chat-close');

    let loaded = false;
    let busy = false;
    // Set when a turn calls a tool that changes data, so the page can be
    // refreshed once rather than after every step.
    let dirty = false;

    // Set just before turnEnded reloads the page, so the panel comes back
    // open on the fresh page. Timestamped so a stale key (a reload that never
    // happened, a tab restored days later) does not pop the panel open.
    const REOPEN_KEY = 'goeat.chat.reopen';
    const REOPEN_WINDOW_MS = 30 * 1000;

    // Which tools alter the household's data. Kept here as well as on the
    // server because the client needs it for one thing only - deciding whether
    // the page underneath is now stale - and shipping the whole registry to
    // the browser for that would be silly.
    const MUTATING = new Set([
        'move_meal', 'swap_meals', 'edit_meal', 'delete_meal', 'fill_slot',
        'set_day_status', 'set_day_headcount',
        'add_list_item', 'remove_list_item', 'mark_already_have', 'set_item_price',
        'set_pantry_qty',
    ]);

    function csrf() {
        const f = document.getElementById('csrf-token');
        if (f) return f.value;
        const anyField = document.querySelector('input[name="gorilla.csrf.Token"]');
        return anyField ? anyField.value : '';
    }

    function el(tag, cls, text) {
        const n = document.createElement(tag);
        if (cls) n.className = cls;
        // textContent everywhere: message content is model output and tool
        // summaries carry user-supplied meal names.
        if (text !== undefined) n.textContent = text;
        return n;
    }

    function scrollDown() {
        log.scrollTop = log.scrollHeight;
    }

    // The "working on it" indicator: a lightbulb in a spinning ring, kept as
    // the last thing in the log for as long as a turn is running. Built once
    // and moved, so it does not restart its animation on every step.
    let thinking = null;

    function showThinking() {
        if (!thinking) {
            thinking = el('div', 'chat-thinking');
            thinking.setAttribute('role', 'status');
            const orb = el('span', 'chat-thinking__orb');
            // Static markup (mdi-lightbulb-outline), no model output in it.
            orb.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">' +
                '<path d="M12,2A7,7 0 0,1 19,9C19,11.38 17.81,13.47 16,14.74V17A1,1 0 0,1 15,18H9A1,1 0 0,1 8,17V14.74C6.19,13.47 5,11.38 5,9A7,7 0 0,1 12,2M9,21V20H15V21A1,1 0 0,1 14,22H10A1,1 0 0,1 9,21M12,4A5,5 0 0,0 7,9C7,11.05 8.23,12.81 10,13.58V16H14V13.58C15.77,12.81 17,11.05 17,9A5,5 0 0,0 12,4Z"/></svg>';
            thinking.appendChild(orb);
            thinking.appendChild(el('span', null, 'Thinking…'));
        }
        log.appendChild(thinking);
        scrollDown();
    }

    function hideThinking() {
        if (thinking) thinking.remove();
    }

    // Anything added to the log mid-turn goes above the indicator.
    function append(node) {
        if (thinking && thinking.isConnected) log.insertBefore(node, thinking);
        else log.appendChild(node);
        scrollDown();
    }

    function addMessage(role, text) {
        const wrap = el('div', 'chat-msg chat-msg--' + role);
        wrap.appendChild(el('div', 'chat-msg__body', text));
        append(wrap);
        return wrap;
    }

    // One line per tool call. Rendered as it happens rather than summarised at
    // the end: a user watching "Moved Chicken Quesadillas to monday dinner."
    // appear can tell immediately that the assistant understood them.
    function addStep(entry) {
        const row = el('div', 'chat-step' + (entry.error ? ' chat-step--error' : ''));
        row.appendChild(el('span', 'chat-step__tool', entry.tool));
        row.appendChild(el('span', 'chat-step__text', entry.error || entry.summary || ''));
        append(row);
        if (MUTATING.has(entry.tool) && !entry.error) dirty = true;
    }

    // A change the assistant wants to make, shown before it happens. The panel
    // stays busy while this is up: the run is paused mid-turn on the server,
    // and letting a second message start would abandon it.
    function addConfirm(p) {
        const box = el('div', 'chat-confirm');
        box.appendChild(el('div', 'chat-confirm__title', 'Apply this change?'));
        box.appendChild(el('div', 'chat-confirm__tool', p.tool));
        if (p.description) box.appendChild(el('div', 'chat-confirm__desc', p.description));
        if (p.detail) box.appendChild(el('div', 'chat-confirm__detail', p.detail));

        const actions = el('div', 'chat-confirm__actions');
        const no = el('button', 'btn btn-ghost btn-sm', 'No');
        const yes = el('button', 'btn btn-primary btn-sm', 'Apply');
        no.type = 'button';
        yes.type = 'button';

        function answer(approve) {
            // Replace the box with what was decided, so the transcript still
            // reads correctly after a reload.
            box.replaceWith(el('div', 'chat-step', approve ? 'Applied.' : 'Left it alone.'));
            showThinking();
            confirmAnswer(approve);
        }
        no.addEventListener('click', function () { answer(false); });
        yes.addEventListener('click', function () { answer(true); });

        actions.appendChild(no);
        actions.appendChild(yes);
        box.appendChild(actions);
        log.appendChild(box);
        scrollDown();
        yes.focus();
    }

    function confirmAnswer(approve) {
        fetch('/chat/confirm', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
                'X-CSRF-Token': csrf(),
            },
            body: 'approve=' + (approve ? '1' : '0'),
        })
            .then(function (resp) {
                if (!resp.ok || !resp.body) {
                    return resp.json().then(function (d) {
                        throw new Error((d && d.error) || 'That did not go through.');
                    });
                }
                return readStream(resp.body.getReader());
            })
            .catch(function (err) {
                addStep({ tool: 'error', error: err.message || 'Something went wrong.' });
            })
            .finally(function () { turnEnded(); });
    }

    function setBusy(on) {
        busy = on;
        sendBtn.disabled = on;
        input.disabled = on;
        sendBtn.textContent = on ? 'Working…' : 'Send';
        if (on) showThinking(); else hideThinking();
    }

    // A turn is over - unless it is paused on a confirmation, in which case
    // the run is held mid-flight on the server and letting another message
    // start would abandon it (confirmAnswer ends it instead).
    //
    // A finished turn that changed data leaves the page behind the panel
    // stale, so it is reloaded right away and the panel reopens itself with
    // the conversation (history comes from the server). A full reload rather
    // than swapping <main> in place: pages carry inline nonce'd scripts and
    // main.js wires its handlers once on load, so a swapped-in page would
    // look right and half of it would not work.
    function turnEnded() {
        if (log.querySelector('.chat-confirm')) return;
        setBusy(false);
        if (dirty) {
            dirty = false;
            try { sessionStorage.setItem(REOPEN_KEY, String(Date.now())); } catch (_) { /* private mode */ }
            window.location.reload();
            return;
        }
        input.focus();
    }

    function renderHistory(messages) {
        log.innerHTML = '';
        if (!messages.length) {
            const hint = el('div', 'chat-empty');
            hint.appendChild(el('p', null, 'Ask me to change the plan or the shopping list.'));
            const ul = document.createElement('ul');
            [
                'move chicken quesadillas to Monday',
                "we're eating out Friday",
                'what is on the shopping list?',
                'we already have olive oil',
            ].forEach(function (s) {
                const li = document.createElement('li');
                const b = el('button', 'chat-suggestion', s);
                b.type = 'button';
                b.addEventListener('click', function () {
                    input.value = s;
                    input.focus();
                });
                li.appendChild(b);
                ul.appendChild(li);
            });
            hint.appendChild(ul);
            log.appendChild(hint);
            return;
        }
        messages.forEach(function (m) {
            (m.audit || []).forEach(addStep);
            addMessage(m.role, m.content);
        });
        scrollDown();
    }

    function load() {
        if (loaded) return;
        loaded = true;
        fetch('/chat/history', { headers: { Accept: 'application/json' } })
            .then(function (r) { return r.json(); })
            .then(function (d) {
                if (!d || !d.ok) return;
                if (!d.enabled) {
                    log.innerHTML = '';
                    log.appendChild(el('div', 'chat-empty',
                        'No AI provider is configured, so the assistant is off. Set one up in Settings.'));
                    form.hidden = true;
                    return;
                }
                renderHistory(d.messages || []);
            })
            .catch(function () {
                log.appendChild(el('div', 'chat-step chat-step--error', 'Could not load the conversation.'));
            });
    }

    function open() {
        panel.hidden = false;
        launcher.setAttribute('aria-expanded', 'true');
        load();
        input.focus();
    }

    function close() {
        panel.hidden = true;
        launcher.setAttribute('aria-expanded', 'false');
    }

    // Exposed so other UI can hand the assistant a specific job instead of
    // making the person retype context it can already see (a modal's "Ask AI"
    // button, say). Opens the panel and fills the box; it does not send on its
    // own, since a delegated action can change real data and deserves a look
    // before it goes.
    window.goeat = window.goeat || {};
    window.goeat.askAI = function (text) {
        open();
        if (!text) return;
        input.value = text;
        input.focus();
        input.setSelectionRange(input.value.length, input.value.length);
    };

    launcher.addEventListener('click', function () {
        if (panel.hidden) open(); else close();
    });
    closeBtn.addEventListener('click', close);

    try {
        const at = Number(sessionStorage.getItem(REOPEN_KEY));
        sessionStorage.removeItem(REOPEN_KEY);
        if (at && Date.now() - at < REOPEN_WINDOW_MS) open();
    } catch (_) { /* private mode */ }

    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && !panel.hidden && !busy) close();
    });

    clearBtn.addEventListener('click', function () {
        window.goeat.confirm('Clear this conversation? The changes it made stay.', {
            title: 'Clear conversation', confirmLabel: 'Clear', danger: true,
        }).then(function (ok) {
            if (!ok) return;
            fetch('/chat/clear', { method: 'POST', headers: { 'X-CSRF-Token': csrf() } })
                .then(function () { renderHistory([]); });
        });
    });

    // Enter sends, Shift+Enter is a newline - the convention every chat box
    // uses, and the textarea exists for the occasional long request.
    input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            form.requestSubmit();
        }
    });

    form.addEventListener('submit', function (e) {
        e.preventDefault();
        if (busy) return;
        const msg = input.value.trim();
        if (!msg) return;

        const empty = log.querySelector('.chat-empty');
        if (empty) empty.remove();

        addMessage('user', msg);
        input.value = '';
        setBusy(true);

        // fetch + manual SSE parse rather than EventSource: EventSource is
        // GET-only and cannot carry the CSRF header, and this request is a
        // POST that changes data.
        fetch('/chat/send', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
                'X-CSRF-Token': csrf(),
            },
            body: 'message=' + encodeURIComponent(msg),
        })
            .then(function (resp) {
                if (!resp.ok || !resp.body) {
                    return resp.json().then(function (d) {
                        throw new Error((d && d.error) || 'The assistant is unavailable.');
                    });
                }
                return readStream(resp.body.getReader());
            })
            .catch(function (err) {
                addStep({ tool: 'error', error: err.message || 'Something went wrong.' });
            })
            .finally(turnEnded);
    });

    // Minimal SSE reader: events arrive as "event: X\ndata: {...}\n\n".
    function readStream(reader) {
        const decoder = new TextDecoder();
        let buf = '';

        function pump() {
            return reader.read().then(function (res) {
                if (res.done) return;
                buf += decoder.decode(res.value, { stream: true });

                let idx;
                while ((idx = buf.indexOf('\n\n')) >= 0) {
                    const chunk = buf.slice(0, idx);
                    buf = buf.slice(idx + 2);
                    handleEvent(chunk);
                }
                return pump();
            });
        }
        return pump();
    }

    function handleEvent(chunk) {
        let name = 'message';
        let data = '';
        chunk.split('\n').forEach(function (line) {
            if (line.startsWith('event: ')) name = line.slice(7).trim();
            else if (line.startsWith('data: ')) data += line.slice(6);
        });
        if (!data) return;

        let payload;
        try { payload = JSON.parse(data); } catch (_) { return; }

        if (name === 'step') addStep(payload);
        else if (name === 'done') { hideThinking(); addMessage('assistant', payload.content); }
        else if (name === 'confirm') { hideThinking(); addConfirm(payload); }
        else if (name === 'error') addStep({ tool: 'error', error: payload.error });
    }
})();

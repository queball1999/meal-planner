/**
 * chat.js - the site-wide assistant panel.
 *
 * The assistant makes real changes to the household's plan, so two things
 * matter more than the chat feel: every tool call it makes is shown as it
 * happens, and the page is reloaded once a turn that changed something
 * finishes - otherwise the user is looking at a plan that no longer matches
 * the database and does not know it.
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

    function addMessage(role, text) {
        const wrap = el('div', 'chat-msg chat-msg--' + role);
        wrap.appendChild(el('div', 'chat-msg__body', text));
        log.appendChild(wrap);
        scrollDown();
        return wrap;
    }

    // One line per tool call. Rendered as it happens rather than summarised at
    // the end: a user watching "Moved Chicken Quesadillas to monday dinner."
    // appear can tell immediately that the assistant understood them.
    function addStep(entry) {
        const row = el('div', 'chat-step' + (entry.error ? ' chat-step--error' : ''));
        row.appendChild(el('span', 'chat-step__tool', entry.tool));
        row.appendChild(el('span', 'chat-step__text', entry.error || entry.summary || ''));
        log.appendChild(row);
        scrollDown();
        if (MUTATING.has(entry.tool) && !entry.error) dirty = true;
    }

    function setBusy(on) {
        busy = on;
        sendBtn.disabled = on;
        input.disabled = on;
        sendBtn.textContent = on ? 'Working…' : 'Send';
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
        // A turn that changed data leaves the page behind it stale. Reload on
        // close rather than mid-conversation, so the panel does not vanish
        // out from under someone who is still typing.
        if (dirty) window.location.reload();
    }

    launcher.addEventListener('click', function () {
        if (panel.hidden) open(); else close();
    });
    closeBtn.addEventListener('click', close);

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
            .finally(function () { setBusy(false); input.focus(); });
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
        else if (name === 'done') addMessage('assistant', payload.content);
        else if (name === 'error') addStep({ tool: 'error', error: payload.error });
    }
})();

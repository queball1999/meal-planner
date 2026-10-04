/**
 * modal.js - Go Eat's own dialog layer.
 *
 * Two things live here:
 *
 *   1. Named modals declared in a template via partials/modal_open.html /
 *      modal_close.html, opened with goeat.openModal(id) and closed by the
 *      header x, a backdrop click, or Escape.
 *   2. goeat.confirm/alert/prompt - async replacements for the browser's
 *      native confirm()/alert()/prompt(). Those block the page, cannot be
 *      themed, and read as the *browser* interrupting rather than this app
 *      asking.
 *
 * Loaded as a classic script before main.js (see layout.html), so everything
 * public hangs off window.goeat rather than being exported.
 */

'use strict';

window.goeat = window.goeat || {};

(function () {
    // ── Named modals ──────────────────────────────────────────────────────

    function focusableIn(modal) {
        const sel = 'a[href], button:not([disabled]), input:not([disabled]), ' +
                    'select:not([disabled]), textarea:not([disabled]), [tabindex="0"]';
        return Array.prototype.slice.call(modal.querySelectorAll(sel))
            .filter(function (el) { return el.offsetParent !== null; });
    }

    const returnFocus = new WeakMap();
    const keydownHandlers = new WeakMap();

    function trapAndEscape(overlay, e) {
        if (e.key === 'Escape') {
            e.preventDefault();
            closeModal(overlay.id);
            return;
        }
        if (e.key !== 'Tab') return;
        const modal = overlay.querySelector('.modal-dialog');
        const focusable = modal ? focusableIn(modal) : [];
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) {
            e.preventDefault();
            last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
            e.preventDefault();
            first.focus();
        }
    }

    function openModal(id, opts) {
        const overlay = document.getElementById(id);
        if (!overlay) return;
        if (opts && opts.onOpen) opts.onOpen(overlay);
        // The overlay markup is emitted inside <main> (partials/modal_open.html
        // in the page's content block). Since we mark <main> inert while a
        // modal is open, a modal left in that subtree goes inert too - every
        // button dead, Escape included, with no way out. Hoisting it to <body>
        // (where the confirm/alert dialog already lives) keeps it interactive.
        // Node move preserves child form state and listeners.
        if (overlay.parentElement !== document.body) {
            document.body.appendChild(overlay);
        }
        dirtyModals.delete(overlay); // a reopened modal starts clean
        overlay.classList.add('active');
        const main = document.querySelector('main');
        if (main) main.setAttribute('inert', '');

        returnFocus.set(overlay, document.activeElement);
        const modal = overlay.querySelector('.modal-dialog');
        const focusable = modal ? focusableIn(modal) : [];
        const target = focusable[0] || modal;
        if (target) target.focus();

        const handler = function (e) { trapAndEscape(overlay, e); };
        keydownHandlers.set(overlay, handler);
        overlay.addEventListener('keydown', handler);
    }

    function closeModal(id) {
        const overlay = document.getElementById(id);
        if (!overlay) return;
        overlay.classList.remove('active');
        dirtyModals.delete(overlay);
        const main = document.querySelector('main');
        if (main) main.removeAttribute('inert');

        const handler = keydownHandlers.get(overlay);
        if (handler) overlay.removeEventListener('keydown', handler);
        keydownHandlers.delete(overlay);

        const toFocus = returnFocus.get(overlay);
        if (toFocus && document.body.contains(toFocus)) toFocus.focus();
        returnFocus.delete(overlay);
    }

    // Close controls are delegated from document rather than bound per element:
    // idempotent however many times a page wires itself, and it covers a modal
    // added to the DOM after load (the item quick-add dialog, the day-status
    // dialog). The confirm/alert overlay below is deliberately excluded - it
    // carries neither data-modal nor data-modal-close, because each of its
    // dismissal paths has to resolve a Promise closeModal() knows nothing about.
    document.addEventListener('click', function (e) {
        if (!(e.target instanceof Element)) return;
        const closer = e.target.closest('[data-modal-close]');
        if (closer) {
            const overlay = closer.closest('[data-modal]');
            if (overlay) {
                closeModal(overlay.id);
                return;
            }
        }
        // A click landing on the overlay itself is a click outside the dialog:
        // .modal-dialog is a child, so anything inside it stops here with e.target
        // being that child instead.
        if (e.target instanceof HTMLElement && e.target.matches('[data-modal]')) {
            // The press must have started on the backdrop too: a text selection
            // or slider drag that begins inside the dialog and is released out
            // in the free space fires its click on the overlay, and must not
            // close the dialog mid-gesture.
            if (!pressStartedOnBackdrop) return;
            // An edited modal refuses a backdrop dismissal (the one close
            // nobody aims at) and says so; × , Cancel and Escape still work.
            // `data-modal-allow-dirty-close` opts a modal out.
            if (dirtyModals.has(e.target) && !e.target.hasAttribute('data-modal-allow-dirty-close')) {
                if (window.showToast) window.showToast('Cannot close: edits have been made. Use Cancel or the x to discard.', 'info');
                return;
            }
            closeModal(e.target.id);
        }
    });

    // Where the current press began. pointerdown covers mouse, touch and pen.
    let pressStartedOnBackdrop = false;
    document.addEventListener('pointerdown', function (e) {
        pressStartedOnBackdrop = e.target instanceof HTMLElement && e.target.matches('[data-modal]');
    }, true);

    // Dirtiness comes from trusted input/change events, never from diffing
    // values against a snapshot taken at open: modals that fill themselves
    // after opening (the generate dialog's pantry list) would otherwise look
    // edited before anyone touched them. Assigning .value fires no events,
    // so programmatic prefill and reset stay invisible to this.
    const dirtyModals = new WeakSet();
    function markDirty(e) {
        if (!e.isTrusted || !(e.target instanceof Element)) return;
        // Choices.js's search box is a real input: typing to filter a
        // dropdown is not an edit.
        if (e.target.closest('.choices__input, [data-modal-ignore-dirty]')) return;
        const overlay = e.target.closest('[data-modal].active');
        if (overlay) dirtyModals.add(overlay);
    }
    document.addEventListener('input', markDirty, true);
    document.addEventListener('change', markDirty, true);
    // For edits that arrive as a button click rather than an input event
    // (adding a row to a list), so a stray backdrop click can't throw it away.
    window.goeat.markModalDirty = function (el) {
        const overlay = el && el.closest('[data-modal].active');
        if (overlay) dirtyModals.add(overlay);
    };

    // Any element with data-modal-open="someModalId" opens that modal.
    //
    // Guarded on the *target* being one of this system's overlays, not on the
    // attribute alone: stores.html predates this file and drives its own
    // `hidden`-toggled panels through the same attribute name. Opening one of
    // those from here would set `inert` on <main> - which contains them - and
    // leave the panel unusable.
    document.addEventListener('click', function (e) {
        if (!(e.target instanceof Element)) return;
        const trigger = e.target.closest('[data-modal-open]');
        if (!trigger) return;
        const target = document.getElementById(trigger.getAttribute('data-modal-open'));
        if (!target || !target.hasAttribute('data-modal')) return;
        e.preventDefault();
        openModal(target.id);
    });

    // ── confirm / alert / prompt ──────────────────────────────────────────

    let dialogParts = null;

    function buildDialog() {
        if (dialogParts) return dialogParts;

        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.id = 'appDialog';

        const modal = document.createElement('div');
        modal.className = 'modal-dialog';
        modal.setAttribute('role', 'alertdialog');
        modal.setAttribute('aria-modal', 'true');
        modal.setAttribute('aria-labelledby', 'appDialog-title');
        modal.tabIndex = -1;

        const header = document.createElement('div');
        header.className = 'modal-dialog__header';
        const title = document.createElement('h2');
        title.id = 'appDialog-title';
        header.appendChild(title);

        const body = document.createElement('div');
        body.className = 'modal-dialog__body';
        const message = document.createElement('p');
        message.style.whiteSpace = 'pre-wrap';
        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'input';
        input.style.width = '100%';
        input.style.marginTop = '0.6rem';
        body.appendChild(message);
        body.appendChild(input);

        const footer = document.createElement('div');
        footer.className = 'modal-dialog__footer';
        const cancelBtn = document.createElement('button');
        cancelBtn.type = 'button';
        cancelBtn.className = 'btn btn-ghost';
        cancelBtn.textContent = 'Cancel';
        const okBtn = document.createElement('button');
        okBtn.type = 'button';
        okBtn.className = 'btn btn-primary';
        footer.appendChild(cancelBtn);
        footer.appendChild(okBtn);

        modal.appendChild(header);
        modal.appendChild(body);
        modal.appendChild(footer);
        overlay.appendChild(modal);
        document.body.appendChild(overlay);

        dialogParts = {
            overlay: overlay, modal: modal, title: title, message: message,
            input: input, cancelBtn: cancelBtn, okBtn: okBtn,
        };
        return dialogParts;
    }

    function showDialog(o) {
        const d = buildDialog();
        d.title.textContent = o.title;
        d.message.textContent = o.message;
        d.input.hidden = o.kind !== 'prompt';
        d.input.type = o.inputType || 'text';
        d.input.value = o.kind === 'prompt' ? (o.defaultValue || '') : '';
        d.cancelBtn.hidden = o.kind === 'alert';
        d.cancelBtn.textContent = o.cancelLabel || 'Cancel';
        d.okBtn.textContent = o.confirmLabel || (o.kind === 'alert' ? 'OK' : 'Confirm');
        // A destructive confirm ("Permanently delete...") gets a red primary so
        // the weight of the choice is visible before it is made.
        d.okBtn.className = o.danger ? 'btn btn-danger' : 'btn btn-primary';

        return new Promise(function (resolve) {
            const previousFocus = document.activeElement;

            function okResult() { return o.kind === 'prompt' ? d.input.value : true; }
            function cancelResult() {
                if (o.kind === 'prompt') return null;
                return o.kind === 'confirm' ? false : undefined;
            }

            function finish(result) {
                d.overlay.classList.remove('active');
                const main = document.querySelector('main');
                if (main) main.removeAttribute('inert');
                document.removeEventListener('keydown', onKeydown, true);
                d.okBtn.removeEventListener('click', onOk);
                d.cancelBtn.removeEventListener('click', onCancel);
                d.overlay.removeEventListener('click', onBackdrop);
                if (previousFocus instanceof HTMLElement && document.body.contains(previousFocus)) {
                    previousFocus.focus();
                }
                resolve(result);
            }
            function onOk() { finish(okResult()); }
            function onCancel() { finish(cancelResult()); }
            function onBackdrop(e) { if (e.target === d.overlay) finish(cancelResult()); }
            function onKeydown(e) {
                if (e.key === 'Escape') {
                    e.preventDefault();
                    finish(cancelResult());
                } else if (e.key === 'Enter' && o.kind === 'prompt' && document.activeElement === d.input) {
                    e.preventDefault();
                    onOk();
                }
            }

            d.okBtn.addEventListener('click', onOk);
            d.cancelBtn.addEventListener('click', onCancel);
            d.overlay.addEventListener('click', onBackdrop);
            document.addEventListener('keydown', onKeydown, true);

            d.overlay.classList.add('active');
            const main = document.querySelector('main');
            if (main) main.setAttribute('inert', '');
            (o.kind === 'prompt' ? d.input : d.okBtn).focus();
        });
    }

    /**
     * Async replacement for confirm(). Resolves true (Confirm) or false
     * (Cancel / Escape / backdrop).
     * @param {string} message
     * @param {{title?: string, confirmLabel?: string, cancelLabel?: string, danger?: boolean}} [opts]
     */
    function confirmDialog(message, opts) {
        opts = opts || {};
        return showDialog({
            kind: 'confirm',
            message: message,
            title: opts.title || 'Confirm',
            confirmLabel: opts.confirmLabel,
            cancelLabel: opts.cancelLabel,
            danger: opts.danger,
        });
    }

    /** Async replacement for alert(). Resolves once dismissed. */
    function alertDialog(message, opts) {
        opts = opts || {};
        return showDialog({ kind: 'alert', message: message, title: opts.title || 'Notice' });
    }

    /** Async replacement for prompt(). Resolves the typed string, or null. */
    function promptDialog(message, opts) {
        opts = opts || {};
        return showDialog({
            kind: 'prompt',
            message: message,
            title: opts.title || 'Input required',
            defaultValue: opts.defaultValue,
            inputType: opts.inputType,
        });
    }

    // ── data-confirm interception ─────────────────────────────────────────
    //
    // Every call site that used to read
    //   onsubmit="return confirm('...')"
    // now reads
    //   data-confirm="..." [data-confirm-danger] [data-confirm-title="..."]
    // on the <form> (or on a <button>/<a> for a non-form action).
    //
    // Because goeat.confirm is async the original event has to be cancelled
    // unconditionally and the action replayed once the promise settles;
    // data-confirmed marks the replay so this handler lets it through.

    function armedFrom(el) {
        return el.closest('[data-confirm]');
    }

    document.addEventListener('submit', function (e) {
        const form = e.target;
        if (!(form instanceof HTMLFormElement)) return;
        if (!form.hasAttribute('data-confirm')) return;
        if (form.dataset.confirmed === '1') {
            delete form.dataset.confirmed;
            return;
        }
        e.preventDefault();
        confirmDialog(form.getAttribute('data-confirm'), {
            title: form.getAttribute('data-confirm-title') || 'Confirm',
            confirmLabel: form.getAttribute('data-confirm-label') || undefined,
            danger: form.hasAttribute('data-confirm-danger'),
        }).then(function (ok) {
            if (!ok) return;
            form.dataset.confirmed = '1';
            // requestSubmit, not submit(): submit() skips the submit event
            // entirely, which would bypass every other delegated handler
            // (autosave's clearTimeout among them).
            if (form.requestSubmit) form.requestSubmit();
            else form.submit();
        });
    }, true);

    // Buttons and links that carry data-confirm but sit outside a form of
    // their own (a link-styled delete action, a formless JS button).
    document.addEventListener('click', function (e) {
        if (!(e.target instanceof Element)) return;
        const el = armedFrom(e.target);
        if (!el || el instanceof HTMLFormElement) return;
        if (el.closest('form[data-confirm]')) return; // the submit handler owns it
        if (el.dataset.confirmed === '1') {
            delete el.dataset.confirmed;
            return;
        }
        e.preventDefault();
        e.stopPropagation();
        confirmDialog(el.getAttribute('data-confirm'), {
            title: el.getAttribute('data-confirm-title') || 'Confirm',
            confirmLabel: el.getAttribute('data-confirm-label') || undefined,
            danger: el.hasAttribute('data-confirm-danger'),
        }).then(function (ok) {
            if (!ok) return;
            el.dataset.confirmed = '1';
            el.click();
        });
    }, true);

    window.goeat.openModal = openModal;
    window.goeat.closeModal = closeModal;
    window.goeat.confirm = confirmDialog;
    window.goeat.alert = alertDialog;
    window.goeat.prompt = promptDialog;
})();

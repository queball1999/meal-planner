/**
 * hovercard.js - meal preview on hover, pinned detail card on click.
 *
 * One floating card for the whole page, moved to whatever is hovered. A
 * calendar month can show sixty meals, and a card per meal would be sixty
 * fetches and sixty DOM subtrees for something the reader looks at one of.
 *
 * Two modes:
 *   - hover: a quick preview that closes the moment the pointer leaves.
 *   - pinned: a left-click on the meal opens the same card and keeps it open
 *     (a second click, the close button, Escape, or an outside click dismiss
 *     it). Pinned, the card grows a small action bar: show the meal on the
 *     week plan (flashes it in place), open the recipe, close.
 *
 * `position: fixed`, positioned at show time. The calendar grid and every
 * table on this app sit inside scrolling containers, and an absolutely
 * positioned descendant is clipped by its nearest scrolling ancestor's padding
 * box regardless of z-index - the same trap tooltip.js documents. A fixed card
 * cannot follow its trigger, so a hover card closes on scroll; a pinned one is
 * repositioned instead.
 */

'use strict';

window.goeat = window.goeat || {};

(function () {
    // Long enough that sweeping the cursor across a week of meals fires
    // nothing, short enough that pausing on one feels immediate.
    const OPEN_DELAY = 350;
    const CLOSE_DELAY = 120;
    const FLASH_MS = 1600;

    const ICON_CLOSE = '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M19,6.41L17.59,5L12,10.59L6.41,5L5,6.41L10.59,12L5,17.59L6.41,19L12,13.41L17.59,19L19,17.59L13.41,12L19,6.41Z"/></svg>';
    const ICON_EYE = '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12,9A3,3 0 0,1 15,12A3,3 0 0,1 12,15A3,3 0 0,1 9,12A3,3 0 0,1 12,9M12,4.5C17,4.5 21.27,7.61 23,12C21.27,16.39 17,19.5 12,19.5C7,19.5 2.73,16.39 1,12C2.73,7.61 7,4.5 12,4.5M3.18,12C4.83,15.36 8.24,17.5 12,17.5C15.76,17.5 19.17,15.36 20.82,12C19.17,8.64 15.76,6.5 12,6.5C8.24,6.5 4.83,8.64 3.18,12Z"/></svg>';
    const ICON_CALENDAR = '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M19,19H5V8H19M16,1V3H8V1H6V3H5C3.89,3 3,3.89 3,5V19A2,2 0 0,0 5,21H19A2,2 0 0,0 21,19V5C21,3.89 20.1,3 19,3H18V1H16M17,12H12V17H17V12Z"/></svg>';

    let card = null;
    let openTimer = null;
    let closeTimer = null;
    let currentTrigger = null;
    let pinned = false; // opened by click - survives mouseout, gets the action bar
    const cache = new Map();

    function build() {
        if (card) return card;
        card = document.createElement('div');
        card.className = 'hovercard';
        card.setAttribute('role', 'tooltip');
        // Keeping the pointer inside the card holds it open, so a reader can
        // move onto it to read a long ingredient list.
        card.addEventListener('mouseenter', function () { clearTimeout(closeTimer); });
        card.addEventListener('mouseleave', function () { if (!pinned) scheduleHide(); });
        document.body.appendChild(card);
        return card;
    }

    function position(trigger) {
        const rect = trigger.getBoundingClientRect();
        const gap = 10;
        const c = build();

        // Measure after the content is in place; width is fixed by CSS but
        // height depends on the ingredient list.
        const box = c.getBoundingClientRect();

        let left = rect.right + gap;
        // Flip to the trigger's left when there is no room on the right.
        if (left + box.width > window.innerWidth - gap) {
            left = rect.left - box.width - gap;
        }
        left = Math.max(gap, Math.min(left, window.innerWidth - box.width - gap));

        let top = rect.top;
        if (top + box.height > window.innerHeight - gap) {
            top = window.innerHeight - box.height - gap;
        }
        top = Math.max(gap, top);

        c.style.left = Math.round(left) + 'px';
        c.style.top = Math.round(top) + 'px';
    }

    function el(tag, cls, text) {
        const n = document.createElement(tag);
        if (cls) n.className = cls;
        // textContent throughout: meal titles and ingredient names are model
        // output and user edits.
        if (text !== undefined) n.textContent = text;
        return n;
    }

    // The pinned-only action bar: show on plan, open recipe, close.
    function actionsBar(data) {
        const bar = el('div', 'hovercard__actions');

        const onPlan = document.createElement('button');
        onPlan.type = 'button';
        onPlan.className = 'hovercard__act';
        onPlan.title = 'Show on the week plan';
        onPlan.setAttribute('aria-label', 'Show on the week plan');
        onPlan.innerHTML = ICON_CALENDAR;
        onPlan.addEventListener('click', function (e) {
            e.stopPropagation();
            showOnPlan(data.id);
        });

        const view = document.createElement('a');
        view.className = 'hovercard__act';
        view.href = data.url || ('/meals/' + data.id);
        view.title = 'Open the recipe';
        view.setAttribute('aria-label', 'Open the recipe');
        view.innerHTML = ICON_EYE;
        view.addEventListener('click', function (e) { e.stopPropagation(); });

        const close = document.createElement('button');
        close.type = 'button';
        close.className = 'hovercard__act';
        close.title = 'Close';
        close.setAttribute('aria-label', 'Close');
        close.innerHTML = ICON_CLOSE;
        close.addEventListener('click', function (e) {
            e.stopPropagation();
            hide();
        });

        bar.appendChild(onPlan);
        bar.appendChild(view);
        bar.appendChild(close);
        return bar;
    }

    function render(data) {
        const c = build();
        c.innerHTML = '';
        if (!data || !data.ok) {
            c.appendChild(el('div', 'hovercard__body', 'No details for this meal.'));
            return;
        }

        if (data.image_url) {
            const img = document.createElement('img');
            img.className = 'hovercard__image';
            img.src = data.image_url;
            img.alt = '';
            img.loading = 'lazy';
            // A missing or broken photo must not leave a torn image icon in
            // the middle of the card.
            img.addEventListener('error', function () { img.remove(); });
            c.appendChild(img);
        }

        const body = el('div', 'hovercard__body');

        // Title and the close/recipe/show-on-plan buttons share one row - on
        // a hover preview as much as a pinned card, so the card behaves the
        // same either way and a reader never has to click just to see them.
        const head = el('div', 'hovercard__head');
        head.appendChild(el('h3', 'hovercard__title', data.title));
        head.appendChild(actionsBar(data));
        body.appendChild(head);

        const meta = el('div', 'hovercard__meta');
        const bits = [];
        if (data.day_label) bits.push(data.day_label + ' ' + data.slot);
        if (data.servings) bits.push(data.servings + ' srv');
        if (data.effort) bits.push(data.effort);
        if (data.is_leftover) bits.push('leftovers');
        meta.textContent = bits.join(' · ');
        body.appendChild(meta);

        if (data.cost) {
            body.appendChild(el('div', 'hovercard__cost', data.cost));
        }

        if (data.ingredients && data.ingredients.length) {
            const ul = el('ul', 'hovercard__ingredients');
            data.ingredients.forEach(function (ing) {
                const li = document.createElement('li');
                li.appendChild(el('span', 'hovercard__ing-name', ing.name));
                if (ing.amount) li.appendChild(el('span', 'hovercard__ing-amount', ing.amount));
                ul.appendChild(li);
            });
            body.appendChild(ul);
            if (data.more) {
                body.appendChild(el('div', 'hovercard__more', '+' + data.more + ' more'));
            }
        }

        c.appendChild(body);
    }

    function show(trigger) {
        const id = trigger.dataset.mealId;
        if (!id) return;
        currentTrigger = trigger;

        function done(raw) {
            const data = Object.assign({ id: id }, raw || {});
            if (!data.url) data.url = '/meals/' + id;
            // The pointer may have moved on during the fetch; rendering then
            // would pop a card for something no longer hovered. A pinned card
            // was opened deliberately, so it renders regardless.
            if (!pinned && currentTrigger !== trigger) return;
            render(data);
            const c = build();
            c.classList.add('hovercard--open');
            c.classList.toggle('hovercard--pinned', pinned);
            position(trigger);
        }

        if (cache.has(id)) { done(cache.get(id)); return; }

        fetch('/meals/' + id + '/card', { headers: { Accept: 'application/json' } })
            .then(function (r) { return r.json(); })
            .then(function (d) { cache.set(id, d); done(d); })
            .catch(function () { /* a preview is not worth an error */ });
    }

    function hide() {
        currentTrigger = null;
        pinned = false;
        if (card) card.classList.remove('hovercard--open', 'hovercard--pinned');
    }

    function scheduleHide() {
        clearTimeout(openTimer);
        clearTimeout(closeTimer);
        closeTimer = setTimeout(function () { if (!pinned) hide(); }, CLOSE_DELAY);
    }

    // Jump to a meal on the week plan and flash it. Prefers the dashboard
    // calendar widget, then the plan board, then anything on the page; with no
    // match on this page (e.g. the meal is not in the visible calendar range)
    // it just navigates to the plan.
    function showOnPlan(id) {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(String(id)) : String(id);
        const sel = '[data-meal-id="' + esc + '"]';
        const target = document.querySelector('.cal-card ' + sel) ||
            document.querySelector('.plan-board ' + sel) ||
            document.querySelector(sel);
        if (!target) { window.location.href = '/plan?flash=' + encodeURIComponent(id); return; }
        hide();
        const cell = target.closest('.cal-meal, .meal-card') || target;
        cell.scrollIntoView({ behavior: 'smooth', block: 'center' });
        cell.classList.add('is-flash');
        setTimeout(function () { cell.classList.remove('is-flash'); }, FLASH_MS);
    }

    // Delegated, so meals added to the DOM after load are covered too.
    document.addEventListener('mouseover', function (e) {
        if (pinned) return;
        if (!(e.target instanceof Element)) return;
        const trigger = e.target.closest('[data-meal-id]:not([data-meal-status-btn])');
        if (!trigger || trigger === currentTrigger) return;
        clearTimeout(closeTimer);
        clearTimeout(openTimer);
        openTimer = setTimeout(function () { show(trigger); }, OPEN_DELAY);
    });

    document.addEventListener('mouseout', function (e) {
        if (pinned) return;
        if (!(e.target instanceof Element)) return;
        if (!e.target.closest('[data-meal-id]:not([data-meal-status-btn])')) return;
        scheduleHide();
    });

    // Left-click on a meal pins the card instead of following the link; the
    // recipe is one click further in, on the card's own eye button. Modified
    // clicks (new tab, etc.) and non-primary buttons are left alone.
    document.addEventListener('click', function (e) {
        if (!(e.target instanceof Element)) return;
        if (card && card.contains(e.target)) return;

        const trigger = e.target.closest('[data-meal-id]:not([data-meal-status-btn])');
        if (trigger) {
            if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
            e.preventDefault();
            clearTimeout(openTimer);
            clearTimeout(closeTimer);
            if (pinned && currentTrigger === trigger) { hide(); return; }
            pinned = true;
            show(trigger);
            return;
        }

        if (pinned) hide();
    });

    // A fixed card cannot follow its trigger. A hover card closes on scroll; a
    // pinned one is nudged back into place instead.
    function onScrollResize() {
        if (!card || !card.classList.contains('hovercard--open')) return;
        if (pinned && currentTrigger) { position(currentTrigger); return; }
        hide();
    }
    window.addEventListener('scroll', onScrollResize, true);
    window.addEventListener('resize', onScrollResize);
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') hide();
    });

    window.goeat.hideHoverCard = hide;
})();

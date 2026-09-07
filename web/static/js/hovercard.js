/**
 * hovercard.js - meal preview on hover.
 *
 * One floating card for the whole page, moved to whatever is hovered. A
 * calendar month can show sixty meals, and a card per meal would be sixty
 * fetches and sixty DOM subtrees for something the reader looks at one of.
 *
 * `position: fixed`, positioned at show time. The calendar grid and every
 * table on this app sit inside scrolling containers, and an absolutely
 * positioned descendant is clipped by its nearest scrolling ancestor's padding
 * box regardless of z-index - the same trap tooltip.js documents. Fixed
 * elements cannot follow their trigger, so the card closes on scroll.
 */

'use strict';

window.goeat = window.goeat || {};

(function () {
    // Long enough that sweeping the cursor across a week of meals fires
    // nothing, short enough that pausing on one feels immediate.
    const OPEN_DELAY = 350;
    const CLOSE_DELAY = 120;

    let card = null;
    let openTimer = null;
    let closeTimer = null;
    let currentTrigger = null;
    const cache = new Map();

    function build() {
        if (card) return card;
        card = document.createElement('div');
        card.className = 'hovercard';
        card.setAttribute('role', 'tooltip');
        // Keeping the pointer inside the card holds it open, so a reader can
        // move onto it to read a long ingredient list.
        card.addEventListener('mouseenter', function () { clearTimeout(closeTimer); });
        card.addEventListener('mouseleave', scheduleHide);
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
        body.appendChild(el('h3', 'hovercard__title', data.title));

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

        if (cache.has(id)) {
            render(cache.get(id));
            build().classList.add('hovercard--open');
            position(trigger);
            return;
        }

        fetch('/meals/' + id + '/card', { headers: { Accept: 'application/json' } })
            .then(function (r) { return r.json(); })
            .then(function (d) {
                cache.set(id, d);
                // The pointer may have moved on during the fetch; rendering
                // then would pop a card for something no longer hovered.
                if (currentTrigger !== trigger) return;
                render(d);
                build().classList.add('hovercard--open');
                position(trigger);
            })
            .catch(function () { /* a preview is not worth an error */ });
    }

    function hide() {
        currentTrigger = null;
        if (card) card.classList.remove('hovercard--open');
    }

    function scheduleHide() {
        clearTimeout(openTimer);
        clearTimeout(closeTimer);
        closeTimer = setTimeout(hide, CLOSE_DELAY);
    }

    // Delegated, so meals added to the DOM after load are covered too.
    document.addEventListener('mouseover', function (e) {
        if (!(e.target instanceof Element)) return;
        const trigger = e.target.closest('[data-meal-id]');
        if (!trigger || trigger === currentTrigger) return;
        clearTimeout(closeTimer);
        clearTimeout(openTimer);
        openTimer = setTimeout(function () { show(trigger); }, OPEN_DELAY);
    });

    document.addEventListener('mouseout', function (e) {
        if (!(e.target instanceof Element)) return;
        if (!e.target.closest('[data-meal-id]')) return;
        scheduleHide();
    });

    // A fixed card cannot follow its trigger, and a touch user has no hover to
    // dismiss it with - the link underneath still works for them.
    window.addEventListener('scroll', hide, true);
    window.addEventListener('resize', hide);
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') hide();
    });

    window.goeat.hideHoverCard = hide;
})();

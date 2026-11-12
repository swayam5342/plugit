'use client';

import { useEffect } from 'react';

const TARGETS = '[data-reveal], .how-field, .tok-chart, .rows, .film-frame';

// Adds .is-in as things scroll into view. Without script (or the observer)
// everything is simply shown: the .js class gates every hidden state.
export default function Reveals() {
  useEffect(() => {
    const els = [...document.querySelectorAll(TARGETS)];
    document.querySelectorAll('.rows').forEach((ul) => {
      [...ul.children].forEach((li, i) => li.style.setProperty('--n', String(i)));
    });
    if (!('IntersectionObserver' in window)) {
      els.forEach((el) => el.classList.add('is-in'));
      return undefined;
    }
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        e.target.classList.add('is-in');
        io.unobserve(e.target);
      }
    }, { rootMargin: '0px 0px -8% 0px', threshold: 0.12 });
    els.forEach((el) => io.observe(el));
    return () => io.disconnect();
  }, []);
  return null;
}

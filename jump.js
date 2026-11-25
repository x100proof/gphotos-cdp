// Evaluated in the Google Photos timeline by jumpAbove (resume.go) with the target time
// ("YYYY-MM-DDTHH:MM:SS"). Scrolls so that every thumbnail on screen is strictly newer than the
// target, as low in the timeline as that allows, plus one screen of margin. The timeline is
// newest-first, so "all visible are newer" is true above the target and false below.
//
// Section heights are estimates until a section loads (a 16,000-photo day starts out tiny and
// grows the timeline by ~900,000 px), and the page shifts scrollTop to keep what's on screen in
// place, so far-apart positions can't be trusted. So: a rough bisection over the whole timeline,
// then a local search from the last position known to be above (step down, doubling the step,
// until below; bisect that gap), always using the position actually reached. Repeat if a
// re-check finds the landing has drifted below the target.
async (T) => {
  const MON = { Jan: 0, Feb: 1, Mar: 2, Apr: 3, May: 4, Jun: 5, Jul: 6, Aug: 7, Sep: 8, Oct: 9, Nov: 10, Dec: 11 };
  // Same patterns as labelTimeMDY / labelTimeDMY in resume.go (en-US / en-GB labels).
  const MDY = /\b(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.? (\d{1,2}), (\d{4}),? (\d{1,2}):(\d{2})(?::(\d{2}))?\s*([AaPp]\.?[Mm])?/;
  const DMY = /\b(\d{1,2}) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.? (\d{4}),? (\d{1,2}):(\d{2})(?::(\d{2}))?\s*([AaPp]\.?[Mm])?/;
  const ts = (label) => {
    let mon, day, m = MDY.exec(label || '');
    if (m) { mon = m[1]; day = m[2]; } else if ((m = DMY.exec(label || ''))) { day = m[1]; mon = m[2]; } else return null;
    let h = +m[4];
    const ap = (m[7] || '').replace('.', '').toUpperCase();
    if (ap) { h %= 12; if (ap === 'PM') h += 12; }
    return Date.UTC(+m[3], MON[mon], +day, h, +m[5], +(m[6] || 0));
  };
  const target = Date.parse(T + 'Z');
  if (Number.isNaN(target)) throw new Error('bad target time ' + T);
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const main = [...document.querySelectorAll('[role="main"]')]
    .filter((x) => x.querySelector('a[href*="/photo/"]') && getComputedStyle(x).visibility != 'hidden')[0];
  if (!main) throw new Error('timeline not found');

  const visible = () => {
    const r = main.getBoundingClientRect();
    return [...main.querySelectorAll('a[href*="/photo/"]')]
      .filter((a) => { const b = a.getBoundingClientRect(); return b.height > 0 && b.bottom > r.top && b.top < r.bottom; })
      .map((a) => ts(a.getAttribute('aria-label')))
      .filter((t) => t !== null);
  };
  // Wait for the thumbnails at the new position to render (same set twice in a row).
  const settle = async () => {
    let prev = null, v = [];
    for (let i = 0; i < 40; i++) {
      await sleep(250);
      v = visible();
      const key = v.join(',');
      if (v.length && key === prev) return v;
      prev = key;
    }
    return v;
  };
  const allNewer = (v) => v.length > 0 && Math.min(...v) > target;
  const iso = (t) => (t == null ? '' : new Date(t).toISOString().slice(0, 19));
  const trace = [];
  const look = async (what) => {
    const v = await settle();
    trace.push([what, main.scrollTop, main.scrollHeight, v.length ? iso(Math.max(...v)) : '', v.length ? iso(Math.min(...v)) : '']);
    return v;
  };

  const H = () => main.clientHeight;
  let steps = 0, rounds = 0;
  const probe = async (y, what) => {
    steps++;
    main.scrollTop = Math.max(0, Math.round(y));
    const v = await look(what);
    // Nothing readable on screen counts as "not above": treating it as below is always safe.
    return { at: main.scrollTop, above: allNewer(v), v };
  };
  // Bisect [lo, hi] (lo above, hi below) down to half a screen.
  const bisect = async (lo, hi, what) => {
    while (hi - lo > H() / 2 && steps < 300) {
      const r = await probe((lo + hi) / 2, what);
      if (r.above) lo = Math.max(lo, Math.min(r.at, hi - 1)); else hi = Math.min(hi, Math.max(r.at, lo + 1));
    }
    return lo;
  };

  let lo = await bisect(0, main.scrollHeight, 'rough');
  let r = null;
  for (; rounds < 12 && steps < 300; rounds++) {
    // A round only counts if the timeline's height didn't change during it (sections that loaded
    // meanwhile move everything below them); otherwise search again from the new landing.
    const h0 = main.scrollHeight;
    r = await probe(lo, 'check');
    if (!r.above) {
      // Drifted below the target: step up, doubling, until above again.
      let d = H();
      while (!r.above && r.at > 0 && steps < 300) { r = await probe(r.at - d, 'up'); d *= 2; }
      if (!r.above) break;
    }
    lo = r.at;
    // Step down from lo, doubling, until below the target (or the end of the timeline).
    let d = H(), hi = null;
    while (steps < 300) {
      const s = await probe(lo + d, 'down');
      if (!s.above) { hi = s.at; break; }
      if (s.at <= lo) break;   // can't scroll further: the end of the timeline
      lo = s.at; d *= 2;
    }
    if (hi === null) { r = await probe(lo, 'end'); if (main.scrollHeight === h0) break; lo = r.at; continue; }
    if (hi - lo > H() / 2) lo = await bisect(lo, hi, 'fine');
    r = await probe(lo, 'land');
    if (r.above) { lo = r.at; if (main.scrollHeight === h0) break; }
  }
  if (!r || (!r.above && main.scrollTop > 0)) throw new Error('could not land above the target: ' + JSON.stringify(trace.slice(-8)));
  main.scrollBy(0, -H()); // one screen of margin
  const v = await look('margin');
  return {
    steps, rounds, trace: window.__jumpTrace ? trace : undefined, // set window.__jumpTrace to debug
    pos: main.scrollTop / Math.max(1, main.scrollHeight - main.clientHeight),
    newest: v.length ? iso(Math.max(...v)) : '',
    oldest: v.length ? iso(Math.min(...v)) : '',
  };
}

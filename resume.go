package main

// Chunked backfill (x100proof fork): -n stops after N new photos and records where in -resume;
// the next run jumps straight there (jumpAbove) instead of scrolling past every downloaded photo.
// -retry / -missed carry photos that failed over to the next run, which opens them directly.

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
)

// Thumbnail times are the photo's local wall-clock time as Google shows it; we keep them as naive
// (UTC-labelled) times and only ever compare them with each other.
const resumeTimeLayout = "2006-01-02T15:04:05"

// Thumbnail aria-label times, as Google writes them for the account's language:
//
//	en-US "Photo - Landscape - Apr 29, 2026, 10:00:00 AM" (the space before AM/PM may be U+202F)
//	en-GB "Photo – Landscape – 29 Apr 2026, 10:00:00" (24h; September is "Sept")
//
// Keep in sync with jump.js.
var (
	labelTimeMDY = regexp.MustCompile(`\b(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.? (\d{1,2}), (\d{4}),? (\d{1,2}):(\d{2})(?::(\d{2}))?[\s\p{Zs}]*([AaPp]\.?[Mm])?`)
	labelTimeDMY = regexp.MustCompile(`\b(\d{1,2}) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.? (\d{4}),? (\d{1,2}):(\d{2})(?::(\d{2}))?[\s\p{Zs}]*([AaPp]\.?[Mm])?`)
)

var monthAbbr = map[string]time.Month{"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6,
	"Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12}

// labelTime parses the time out of a thumbnail's aria-label.
func labelTime(label string) (time.Time, bool) {
	var mon, dayS string
	var m []string
	if m = labelTimeMDY.FindStringSubmatch(label); m != nil {
		mon, dayS = m[1], m[2]
	} else if m = labelTimeDMY.FindStringSubmatch(label); m != nil {
		dayS, mon = m[1], m[2]
	} else {
		return time.Time{}, false
	}
	day, _ := strconv.Atoi(dayS)
	year, _ := strconv.Atoi(m[3])
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	sec, _ := strconv.Atoi(m[6]) // "" -> 0
	if ampm := strings.ToUpper(strings.ReplaceAll(m[7], ".", "")); ampm != "" {
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		hour %= 12
		if ampm == "PM" {
			hour += 12
		}
	}
	if hour > 23 || minute > 59 || sec > 59 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	return time.Date(year, monthAbbr[mon], day, hour, minute, sec, 0, time.UTC), true
}

// wideMargin: how far above the resume time a "wide" jump lands, when the previous scan never met
// its resume photo (deleted, or the jump landed below it). Generous: thumbnail times are local
// times, so photos from other time zones could in principle be out of order by up to 26h.
const wideMargin = 26 * time.Hour

// readResume reads a -resume file: "<time>\t<photo id>[\twide]". at is "" if the file is missing
// or empty, "end" once the end of the library was reached. wide: the previous run's scan never met
// the photo id (it may have landed below it), so jump wideMargin higher this time.
func readResume(path string) (at, id string, wide bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	} else if err != nil {
		return "", "", false, err
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\t")
	at = strings.TrimSpace(f[0])
	if at == "" || at == "end" {
		return at, "", false, nil
	}
	if _, err := time.Parse(resumeTimeLayout, at); err != nil {
		return "", "", false, fmt.Errorf("bad -resume file %s: %q", path, at)
	}
	if len(f) > 1 {
		id = strings.TrimSpace(f[1])
	}
	return at, id, len(f) > 2 && strings.TrimSpace(f[2]) == "wide", nil
}

func readIdList(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	var ids []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id, _, _ := strings.Cut(strings.TrimSpace(sc.Text()), "\t")
		if id != "" && !seen[id] && !strings.ContainsAny(id, "/.") {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, sc.Err()
}

// hasFileOnDisk: like dirHasFiles, but looks at the disk now (not the startup listing).
func hasFileOnDisk(downloadDir, imageId string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(downloadDir, imageId))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.Size() > 0 {
			return true, nil
		}
	}
	return false, nil
}

func writeFileAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeMissed writes (to -missed) the ids tried this run that still have no file, except ones
// Google can't serve at all.
func (s *Session) writeMissed(tried []string) {
	var missed []string
	for _, id := range tried {
		if _, ok := s.notDownloadable.Load(id); ok {
			continue
		}
		if has, err := hasFileOnDisk(s.downloadDir, id); err == nil && has {
			continue
		}
		missed = append(missed, id)
	}
	content := ""
	if len(missed) > 0 {
		content = strings.Join(missed, "\n") + "\n"
	}
	if err := writeFileAtomic(*missedFlag, content); err != nil {
		log.Error().Err(err).Msgf("could not write -missed %s", *missedFlag)
		return
	}
	log.Info().Msgf("%d photo(s) tried but not downloaded, listed in %s", len(missed), *missedFlag)
}

// liveLabelTime reads the label time of a photo's thumbnail as rendered now.
func (s *Session) liveLabelTime(ctx context.Context, imageId string) (time.Time, bool) {
	var label string
	js := fmt.Sprintf(`(() => { const a = document.querySelector('a[href*="/photo/' + %q + '"]'); return (a && a.getAttribute('aria-label')) || ''; })()`, imageId)
	if err := chromedp.Evaluate(js, &label).Do(ctx); err != nil {
		log.Debug().Err(err).Msg("reading the stop photo's label")
		return time.Time{}, false
	}
	return labelTime(label)
}

//go:embed jump.js
var jumpJS string

type jumpResult struct {
	Steps  int     `json:"steps"`
	Rounds int     `json:"rounds"`
	Pos    float64 `json:"pos"`
	Newest string  `json:"newest"`
	Oldest string  `json:"oldest"`
}

// jumpAbove scrolls the timeline to just above the newest photo taken at or before at
// (resumeTimeLayout): every photo from there down is then scanned as usual. It bisects on the
// scroll position, reading the thumbnails' times, so landing too high only costs a little rescanning.
func (s *Session) jumpAbove(ctx context.Context, at string) error {
	log.Info().Msgf("jumping to just above %s", at)
	start := time.Now()
	if err := target.ActivateTarget(chromedp.FromContext(ctx).Target.TargetID).Do(ctx); err != nil {
		log.Debug().Err(err).Msg("activate target before jump")
	}
	jctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var res jumpResult
	if err := chromedp.Evaluate(fmt.Sprintf("(%s)(%q)", jumpJS, at), &res,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
	).Do(jctx); err != nil {
		return err
	}
	log.Info().Msgf("jumped in %s (%d bisect steps, %d rounds): at %.2f%% of the timeline, showing %s .. %s",
		time.Since(start).Round(time.Second), res.Steps, res.Rounds, res.Pos*100, res.Newest, res.Oldest)
	return nil
}

// mainScrollerJS evaluates to the timeline's scrolling element (as in setScrollPosition).
const mainScrollerJS = `[...document.querySelectorAll('[role="main"]')].filter(x => x.querySelector('a[href*="/photo/"]') && getComputedStyle(x).visibility != 'hidden')[0]`

// scrollScreens scrolls the timeline by n screens (negative: up) and waits for it to render.
func scrollScreens(ctx context.Context, n int) error {
	if err := chromedp.Evaluate(fmt.Sprintf(`(() => { const m = %s; if (!m) return false; m.scrollBy(0, %d * m.clientHeight); return true; })()`, mainScrollerJS, n), nil).Do(ctx); err != nil {
		return err
	}
	time.Sleep(1500 * time.Millisecond)
	return nil
}

// domOrderJS: how many consecutive photo links (DOM order, which is the scan's order) go visually
// upwards. The scan advances by focusing the last link in DOM order, so the DOM must run top to
// bottom; scrolling up screen by screen can mount the newly shown rows after the others.
const domOrderJS = `(() => { const t = [...document.querySelectorAll(%q)].map(a => a.getBoundingClientRect().top);
  let bad = 0; for (let i = 1; i < t.length; i++) if (t[i] < t[i-1] - 1) bad++; return [bad, t.length]; })()`

// remount re-renders the timeline around the current position: scroll far down and back, so the
// thumbnails are mounted afresh in DOM order (as after a jump).
func (s *Session) remount(ctx context.Context) error {
	var pos float64
	if err := chromedp.Evaluate(fmt.Sprintf(`(() => { const m = %s; const p = m.scrollTop; m.scrollTop = p + 40 * m.clientHeight; return p; })()`, mainScrollerJS), &pos).Do(ctx); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	if err := chromedp.Evaluate(fmt.Sprintf(`(() => { const m = %s; m.scrollTop = %f; return true; })()`, mainScrollerJS, pos), nil).Do(ctx); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	return nil
}

func (s *Session) domOrder(ctx context.Context) (bad, total int) {
	var r []int
	if err := chromedp.Evaluate(fmt.Sprintf(domOrderJS, s.getPhotoNodeSelector()), &r).Do(ctx); err != nil || len(r) != 2 {
		return -1, -1
	}
	return r[0], r[1]
}

// verifyAbove checks a jump independently of jump.js: the first thumbnails the scan will process
// (the first photo links in the page, as resync queries them) must be newer than the resume
// point, or photos between the resume point and the landing would never be scanned. If the jump
// went too far, scroll up a screen at a time until they are; give up (error) after 200 screens.
// After a correction, re-render the view (remount) and check again: the scan got stuck after
// screen-by-screen scrolling up, but not after a jump's direct repositioning.
func (s *Session) verifyAbove(ctx context.Context, at string) error {
	target, err := time.Parse(resumeTimeLayout, at)
	if err != nil {
		return err
	}
	sel := s.getPhotoNodeSelector()
	up, waits, remounts := 0, 0, 0
	for up <= 200 {
		var labels []string
		if err := chromedp.Evaluate(fmt.Sprintf(`[...document.querySelectorAll(%q)].slice(0, 30).map(a => a.getAttribute('aria-label') || '')`, sel), &labels).Do(ctx); err != nil {
			return err
		}
		var first time.Time
		for _, l := range labels {
			if t, ok := labelTime(l); ok {
				first = t
				break
			}
		}
		if first.IsZero() && waits < 20 { // not rendered yet
			waits++
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if !first.IsZero() && first.After(target) {
			bad, total := s.domOrder(ctx)
			if up > 0 && remounts == 0 {
				// Scrolling up a screen at a time left the scan stuck (it advances by focusing
				// the last link in DOM order): re-render the view, then check it once more.
				log.Info().Msgf("corrected by scrolling up %d screen(s) (%d of %d thumbnails out of DOM order); re-rendering the view", up, bad, total)
				remounts++
				waits = 0
				if err := s.remount(ctx); err != nil {
					return err
				}
				continue
			}
			log.Debug().Msgf("%d of %d thumbnails out of DOM order", bad, total)
			if up > 0 {
				log.Warn().Msgf("the jump had gone too far: corrected by scrolling up %d screen(s); the scan now starts at %s, newer than the resume point %s", up, first.Format(resumeTimeLayout), at)
			} else {
				log.Info().Msgf("jump checked: the scan starts at %s, newer than the resume point %s", first.Format(resumeTimeLayout), at)
			}
			return nil
		}
		up++
		waits = 0
		if err := scrollScreens(ctx, -1); err != nil {
			return err
		}
	}
	return fmt.Errorf("the jump went too far and 200 screens up the first photo is still not newer than %s", at)
}

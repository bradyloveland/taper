// Package cal works out when calendar events happen: dates in the school's
// time zone, repeating events, and their occurrences in a range of days.
package cal

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dates are civil dates, held as midnight UTC so day arithmetic never meets
// daylight saving.

// ParseDate reads YYYY-MM-DD.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, errors.New("dates look like 2026-10-31")
	}
	return t, nil
}

// FormatDate writes YYYY-MM-DD.
func FormatDate(t time.Time) string { return t.Format("2006-01-02") }

// ParseClock reads HH:MM (24-hour), returning minutes after midnight.
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, errors.New("times look like 14:30")
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Today is the current date in loc.
func Today(loc *time.Location, now time.Time) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Repeat kinds.
const (
	None    = ""
	Daily   = "daily"
	Weekly  = "weekly"
	Monthly = "monthly"
	Yearly  = "yearly"
)

// maxOccurrences stops runaway expansion.
const maxOccurrences = 5000

// Event is what's needed to work out when something happens.
type Event struct {
	StartDate time.Time // first day of the first occurrence
	EndDate   time.Time // last day (inclusive) of the first occurrence
	StartMin  int       // minutes after midnight, or -1 for all day
	EndMin    int
	Repeat    string
	Days      []time.Weekday // weekly: which days (empty means StartDate's weekday)
	Until     time.Time      // last day an occurrence may start (zero: no end)
	Skips     map[string]bool
}

// AllDay reports whether the event has no times.
func (e *Event) AllDay() bool { return e.StartMin < 0 }

// span is how many days after its start an occurrence ends.
func (e *Event) span() int { return int(e.EndDate.Sub(e.StartDate).Hours() / 24) }

// Occurrences returns the start dates of occurrences that touch any day
// from..to (inclusive), in order.
func (e *Event) Occurrences(from, to time.Time) []time.Time {
	span := e.span()
	lo := from.AddDate(0, 0, -span) // an occurrence starting here still reaches from
	hi := to
	if !e.Until.IsZero() && e.Until.Before(hi) {
		hi = e.Until
	}
	var out []time.Time
	add := func(d time.Time) bool {
		if d.Before(e.StartDate) || d.Before(lo) || d.After(hi) || e.Skips[FormatDate(d)] {
			return len(out) < maxOccurrences
		}
		out = append(out, d)
		return len(out) < maxOccurrences
	}
	switch e.Repeat {
	case None:
		if !e.StartDate.After(to) && !e.EndDate.Before(from) && !e.Skips[FormatDate(e.StartDate)] {
			out = append(out, e.StartDate)
		}
	case Daily, Weekly:
		days := map[time.Weekday]bool{}
		for _, d := range e.Days {
			days[d] = true
		}
		if len(days) == 0 {
			days[e.StartDate.Weekday()] = true
		}
		d := e.StartDate
		if lo.After(d) {
			d = lo
		}
		for ; !d.After(hi); d = d.AddDate(0, 0, 1) {
			if e.Repeat == Weekly && !days[d.Weekday()] {
				continue
			}
			if !add(d) {
				break
			}
		}
	case Monthly:
		day := e.StartDate.Day()
		start := e.StartDate
		months := 0
		if lo.After(start) {
			months = (lo.Year()-start.Year())*12 + int(lo.Month()-start.Month()) - 1
			if months < 0 {
				months = 0
			}
		}
		for ; ; months++ {
			first := time.Date(start.Year(), start.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
			if first.After(hi) {
				break
			}
			d := time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
			if d.Month() != first.Month() {
				continue // no 31st in this month
			}
			if !add(d) {
				break
			}
		}
	case Yearly:
		start := e.StartDate
		y := start.Year()
		if lo.Year()-1 > y {
			y = lo.Year() - 1
		}
		for ; y <= hi.Year(); y++ {
			d := time.Date(y, start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
			if d.Month() != start.Month() {
				continue // 29 February in a common year
			}
			if !add(d) {
				break
			}
		}
	}
	return out
}

// Instant turns a date and minutes after midnight in loc into a time.
func Instant(date time.Time, minutes int, loc *time.Location) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), minutes/60, minutes%60, 0, 0, loc)
}

// Bounds returns when an occurrence starting on date begins and ends. All-day
// occurrences run from midnight of the first day to midnight after the last.
func (e *Event) Bounds(date time.Time, loc *time.Location) (time.Time, time.Time) {
	end := date.AddDate(0, 0, e.span())
	if e.AllDay() {
		return Instant(date, 0, loc), Instant(end.AddDate(0, 0, 1), 0, loc)
	}
	return Instant(date, e.StartMin, loc), Instant(end, e.EndMin, loc)
}

// ParseDays reads "1,3" into weekdays.
func ParseDays(s string) []time.Weekday {
	var out []time.Weekday
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err == nil && n >= 0 && n <= 6 {
			out = append(out, time.Weekday(n))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FormatDays writes weekdays as "1,3".
func FormatDays(days []time.Weekday) string {
	parts := make([]string, len(days))
	for i, d := range days {
		parts[i] = strconv.Itoa(int(d))
	}
	return strings.Join(parts, ",")
}

// Describe says how an event repeats, like "Every week on Tuesday and
// Thursday, until June 5, 2027".
func (e *Event) Describe() string {
	var s string
	switch e.Repeat {
	case None:
		return ""
	case Daily:
		s = "Every day"
	case Weekly:
		days := e.Days
		if len(days) == 0 {
			days = []time.Weekday{e.StartDate.Weekday()}
		}
		names := make([]string, len(days))
		for i, d := range days {
			names[i] = d.String()
		}
		s = "Every week on " + joinAnd(names)
	case Monthly:
		s = fmt.Sprintf("Every month on the %s", ordinal(e.StartDate.Day()))
	case Yearly:
		s = "Every year on " + e.StartDate.Format("January 2")
	}
	if !e.Until.IsZero() {
		s += ", until " + e.Until.Format("January 2, 2006")
	}
	return s
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

package cal

import (
	"strings"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return t
}

func dates(ts []time.Time) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = FormatDate(t)
	}
	return strings.Join(parts, " ")
}

func TestOneOff(t *testing.T) {
	e := Event{StartDate: d("2026-10-10"), EndDate: d("2026-10-12"), StartMin: -1}
	if got := dates(e.Occurrences(d("2026-10-01"), d("2026-10-31"))); got != "2026-10-10" {
		t.Fatalf("got %q", got)
	}
	// A three-day event touching the range from either side.
	if got := dates(e.Occurrences(d("2026-10-12"), d("2026-10-20"))); got != "2026-10-10" {
		t.Fatalf("overlap at the end: %q", got)
	}
	if got := dates(e.Occurrences(d("2026-10-13"), d("2026-10-20"))); got != "" {
		t.Fatalf("after: %q", got)
	}
}

func TestWeekly(t *testing.T) {
	// Tuesdays and Thursdays from Tue 6 Oct 2026, until 22 Oct, skipping the 15th.
	e := Event{StartDate: d("2026-10-06"), EndDate: d("2026-10-06"), StartMin: 600, EndMin: 690, Repeat: Weekly,
		Days: []time.Weekday{time.Tuesday, time.Thursday}, Until: d("2026-10-22"), Skips: map[string]bool{"2026-10-15": true}}
	got := dates(e.Occurrences(d("2026-10-01"), d("2026-10-31")))
	if got != "2026-10-06 2026-10-08 2026-10-13 2026-10-20 2026-10-22" {
		t.Fatalf("got %q", got)
	}
	if e.Describe() != "Every week on Tuesday and Thursday, until October 22, 2026" {
		t.Fatalf("describe %q", e.Describe())
	}
	// No days given: the start date's weekday.
	e2 := Event{StartDate: d("2026-10-07"), EndDate: d("2026-10-07"), StartMin: -1, Repeat: Weekly}
	if got := dates(e2.Occurrences(d("2026-10-01"), d("2026-10-21"))); got != "2026-10-07 2026-10-14 2026-10-21" {
		t.Fatalf("default weekday: %q", got)
	}
	// Far in the future it stays cheap and correct.
	if got := dates(e2.Occurrences(d("2036-01-01"), d("2036-01-10"))); got != "2036-01-02 2036-01-09" {
		t.Fatalf("future: %q", got)
	}
}

func TestDailyMonthlyYearly(t *testing.T) {
	daily := Event{StartDate: d("2026-12-30"), EndDate: d("2026-12-30"), StartMin: -1, Repeat: Daily, Until: d("2027-01-02")}
	if got := dates(daily.Occurrences(d("2026-12-01"), d("2027-12-31"))); got != "2026-12-30 2026-12-31 2027-01-01 2027-01-02" {
		t.Fatalf("daily: %q", got)
	}
	monthly := Event{StartDate: d("2026-01-31"), EndDate: d("2026-01-31"), StartMin: 540, EndMin: 600, Repeat: Monthly}
	if got := dates(monthly.Occurrences(d("2026-01-01"), d("2026-05-31"))); got != "2026-01-31 2026-03-31 2026-05-31" {
		t.Fatalf("monthly skips short months: %q", got)
	}
	if got := dates(monthly.Occurrences(d("2030-07-01"), d("2030-08-31"))); got != "2030-07-31 2030-08-31" {
		t.Fatalf("monthly far ahead: %q", got)
	}
	if monthly.Describe() != "Every month on the 31st" {
		t.Fatal(monthly.Describe())
	}
	leap := Event{StartDate: d("2028-02-29"), EndDate: d("2028-02-29"), StartMin: -1, Repeat: Yearly}
	if got := dates(leap.Occurrences(d("2028-01-01"), d("2033-12-31"))); got != "2028-02-29 2032-02-29" {
		t.Fatalf("yearly leap day: %q", got)
	}
	if ordinal(1) != "1st" || ordinal(2) != "2nd" || ordinal(3) != "3rd" || ordinal(11) != "11th" || ordinal(22) != "22nd" {
		t.Fatal("ordinals")
	}
}

func TestBoundsAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	e := Event{StartDate: d("2026-10-27"), EndDate: d("2026-10-27"), StartMin: 600, EndMin: 690, Repeat: Weekly}
	occ := e.Occurrences(d("2026-10-27"), d("2026-11-10"))
	for _, o := range occ {
		s, end := e.Bounds(o, loc)
		if s.Hour() != 10 || end.Hour() != 11 || end.Minute() != 30 {
			t.Fatalf("%s: %v %v", FormatDate(o), s, end)
		}
	}
	// The UTC hour changes when daylight saving ends on 1 Nov.
	s1, _ := e.Bounds(occ[0], loc)
	s2, _ := e.Bounds(occ[1], loc)
	if s1.UTC().Hour() == s2.UTC().Hour() {
		t.Fatal("wall-clock time should be kept across DST")
	}
	allDay := Event{StartDate: d("2026-12-24"), EndDate: d("2026-12-26"), StartMin: -1}
	s, end := allDay.Bounds(d("2026-12-24"), loc)
	if FormatDate(s) != "2026-12-24" || end.Day() != 27 || end.Hour() != 0 {
		t.Fatalf("all-day bounds %v %v", s, end)
	}
}

func TestParsing(t *testing.T) {
	if _, err := ParseDate("2026-13-01"); err == nil {
		t.Fatal("bad date accepted")
	}
	if m, err := ParseClock("14:30"); err != nil || m != 870 {
		t.Fatal("clock")
	}
	if _, err := ParseClock("25:00"); err == nil {
		t.Fatal("bad time accepted")
	}
	if FormatDays(ParseDays("4, 2,9,x")) != "2,4" {
		t.Fatal("days")
	}
	loc, _ := time.LoadLocation("Pacific/Auckland")
	if FormatDate(Today(loc, time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC))) != "2026-10-08" {
		t.Fatal("today in a time zone")
	}
}

func TestICS(t *testing.T) {
	var b strings.Builder
	loc, _ := time.LoadLocation("America/Denver")
	items := []FeedItem{
		{UID: "a-20261006@taper", AllDay: false, Start: time.Date(2026, 10, 6, 10, 0, 0, 0, loc), End: time.Date(2026, 10, 6, 11, 30, 0, 0, loc),
			Summary: "History; Liberty, and law", Description: "Line one\nLine two " + strings.Repeat("é", 60), Location: "Room 2", Categories: "History of Liberty"},
		{UID: "b@taper", AllDay: true, Start: d("2026-12-24"), End: d("2026-12-27"), Summary: "Winter break"},
	}
	if err := WriteICS(&b, "Kindred: My calendar", "", items); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "X-WR-CALNAME:Kindred: My calendar\r\n",
		"DTSTART:20261006T160000Z\r\n", "DTEND:20261006T173000Z\r\n",
		`SUMMARY:History\; Liberty\, and law`, `DESCRIPTION:Line one\nLine two`,
		"DTSTART;VALUE=DATE:20261224\r\n", "DTEND;VALUE=DATE:20261227\r\n", "END:VCALENDAR\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\r\n") {
		if len(l) > 75 {
			t.Errorf("line longer than 75 octets: %q", l)
		}
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("bare newline in output")
	}
}

package cal

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
)

// FeedItem is one occurrence in an iCalendar feed.
type FeedItem struct {
	UID         string // unique and stable for this occurrence
	Sequence    int
	AllDay      bool
	Start, End  time.Time // timed: instants; all-day: dates, End exclusive
	Summary     string
	Description string
	Location    string
	URL         string
	Categories  string
	Updated     time.Time
}

// WriteICS writes an RFC 5545 calendar. Timed occurrences are in UTC, so no
// time zone definitions are needed and every calendar app agrees.
func WriteICS(w io.Writer, name, desc string, items []FeedItem) error {
	bw := bufio.NewWriter(w)
	line := func(s string) { bw.WriteString(fold(s)) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Taper//Calendar//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:" + escape(name))
	if desc != "" {
		line("X-WR-CALDESC:" + escape(desc))
	}
	line("REFRESH-INTERVAL;VALUE=DURATION:PT1H")
	line("X-PUBLISHED-TTL:PT1H")
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, it := range items {
		line("BEGIN:VEVENT")
		line("UID:" + escape(it.UID))
		line("DTSTAMP:" + stamp)
		if !it.Updated.IsZero() {
			line("LAST-MODIFIED:" + it.Updated.UTC().Format("20060102T150405Z"))
		}
		line("SEQUENCE:" + strconv.Itoa(it.Sequence))
		if it.AllDay {
			line("DTSTART;VALUE=DATE:" + it.Start.Format("20060102"))
			line("DTEND;VALUE=DATE:" + it.End.Format("20060102"))
			line("TRANSP:TRANSPARENT")
		} else {
			line("DTSTART:" + it.Start.UTC().Format("20060102T150405Z"))
			line("DTEND:" + it.End.UTC().Format("20060102T150405Z"))
		}
		line("SUMMARY:" + escape(it.Summary))
		if it.Description != "" {
			line("DESCRIPTION:" + escape(it.Description))
		}
		if it.Location != "" {
			line("LOCATION:" + escape(it.Location))
		}
		if it.URL != "" {
			line("URL:" + it.URL)
		}
		if it.Categories != "" {
			line("CATEGORIES:" + escape(it.Categories))
		}
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return bw.Flush()
}

// escape quotes text values (RFC 5545 3.3.11).
func escape(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`, "\r", "").Replace(s)
}

// fold splits a content line into 75-octet pieces without breaking UTF-8,
// and ends it with CRLF.
func fold(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		size := len(string(r))
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}

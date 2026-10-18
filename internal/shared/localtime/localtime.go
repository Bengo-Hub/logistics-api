// Package localtime is the one time zone logistics counts calendar days in: report periods and
// dates shown to people, such as an insurance expiry. Pods run in UTC, so a day must never be cut
// at UTC midnight (that started "today" at 03:00 in Kenya).
package localtime

import (
	"os"
	"time"
)

// Location is REPORTS_TIMEZONE, default Africa/Nairobi.
var Location = func() *time.Location {
	name := os.Getenv("REPORTS_TIMEZONE")
	if name == "" {
		name = "Africa/Nairobi"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.FixedZone("EAT", 3*60*60)
}()

// Date renders t as YYYY-MM-DD in Location.
func Date(t time.Time) string { return t.In(Location).Format("2006-01-02") }

// EndOfDay returns the last instant of the calendar day of t in Location. A policy "expiring
// 24/03/2027" covers that whole day, so it is stored as 23:59:59 local.
func EndOfDay(t time.Time) time.Time {
	l := t.In(Location)
	return time.Date(l.Year(), l.Month(), l.Day(), 23, 59, 59, 0, Location)
}

// DaysUntil counts calendar days from now to t in Location (0 = today, negative = past).
func DaysUntil(now, t time.Time) int {
	a, b := now.In(Location), t.In(Location)
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, Location)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, Location)
	return int(db.Sub(da).Hours() / 24)
}

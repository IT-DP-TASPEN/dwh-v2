package ingestion

import (
	"encoding/json"
	"errors"
	"time"
)

const dateLayout = "2006-01-02"

// CalendarDate is a civil date. FromTime deliberately preserves the supplied
// clock's Y/M/D fields instead of converting the instant to another location.
type CalendarDate struct{ year, month, day int }

func ParseCalendarDate(value string) (CalendarDate, error) {
	parsed, err := time.Parse(dateLayout, value)
	if err != nil {
		return CalendarDate{}, err
	}
	return CalendarDate{year: parsed.Year(), month: int(parsed.Month()), day: parsed.Day()}, nil
}

func CalendarDateFromTime(value time.Time) CalendarDate {
	year, month, day := value.Date()
	return CalendarDate{year: year, month: int(month), day: day}
}

func (date CalendarDate) String() string {
	if date.IsZero() {
		return ""
	}
	return time.Date(date.year, time.Month(date.month), date.day, 0, 0, 0, 0, time.UTC).Format(dateLayout)
}

func (date CalendarDate) IsZero() bool { return date.year == 0 }

func (date CalendarDate) AddDays(days int) CalendarDate {
	if date.IsZero() {
		return date
	}
	return CalendarDateFromTime(time.Date(date.year, time.Month(date.month), date.day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days))
}

func (date CalendarDate) MarshalJSON() ([]byte, error) {
	if date.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(date.String())
}

func (date *CalendarDate) UnmarshalJSON(data []byte) error {
	if date == nil {
		return errors.New("nil CalendarDate")
	}
	if string(data) == "null" {
		*date = CalendarDate{}
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := ParseCalendarDate(value)
	if err != nil {
		return err
	}
	*date = parsed
	return nil
}

type FixedDateRangeParams struct{ From, To CalendarDate }
type FixedSnapshotDateParams struct{ Date CalendarDate }
type DetailSnapshotParams struct{ AsOfDate CalendarDate }

type SchedulePolicy string

const PreviousCalendarDayJakarta SchedulePolicy = "previous_calendar_day_jakarta"

var jakartaLocation = time.FixedZone("Asia/Jakarta", 7*60*60)

func ResolvePreviousCalendarDayJakarta(now time.Time) CalendarDate {
	return CalendarDateFromTime(now.In(jakartaLocation).AddDate(0, 0, -1))
}

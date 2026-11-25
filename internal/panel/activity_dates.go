package panel

import (
	"errors"
	"net/url"
	"time"
)

type activityDates struct {
	from, before string
	direction    string
}

func parseActivityDates(query url.Values) (activityDates, error) {
	dates := activityDates{from: query.Get("from"), before: query.Get("before"), direction: "DESC"}
	switch query.Get("sort") {
	case "", "newest":
	case "oldest":
		dates.direction = "ASC"
	default:
		return activityDates{}, errors.New("invalid date sort order")
	}
	var from, before time.Time
	var err error
	if dates.from != "" {
		from, err = time.Parse(time.RFC3339, dates.from)
		if err != nil {
			return activityDates{}, errors.New("invalid start time")
		}
	}
	if dates.before != "" {
		before, err = time.Parse(time.RFC3339, dates.before)
		if err != nil {
			return activityDates{}, errors.New("invalid end time")
		}
	}
	if dates.from != "" && dates.before != "" && !from.Before(before) {
		return activityDates{}, errors.New("end time must follow start time")
	}
	return dates, nil
}

// Bounds are instants. The browser sends local midnight and the midnight
// after the selected last day, so both displayed calendar dates are included.
func (dates activityDates) filter(column string, args []any) (string, []any) {
	var sql string
	if dates.from != "" {
		sql += " AND julianday(" + column + ") >= julianday(?)"
		args = append(args, dates.from)
	}
	if dates.before != "" {
		sql += " AND julianday(" + column + ") < julianday(?)"
		args = append(args, dates.before)
	}
	return sql, args
}

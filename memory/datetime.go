package memory

import "time"

// Datetime normalization rules shared across memory layers.
//
// Mirrors src/octop_memory/domain/datetime.py.

// datetimeLayouts lists the timestamp layouts accepted by
// ParseDatetimeUTC, mirroring Python's lenient datetime.fromisoformat
// (3.11+): optional fractional seconds, "T" or space separator, "Z" or
// ±HH:MM / ±HHMM / ±HH offsets, date-only values. Values without a
// timezone suffix are interpreted as UTC by AsUtc semantics.
var datetimeLayouts = []string{
	time.RFC3339Nano,                      // 2006-01-02T15:04:05.999999999Z07:00
	time.RFC3339,                          // 2006-01-02T15:04:05Z07:00
	"2006-01-02T15:04:05.999999999Z0700",  // compact offset
	"2006-01-02T15:04:05.999999999",       // naive, T-separated
	"2006-01-02 15:04:05.999999999Z07:00", // space-separated with offset
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999", // naive, space-separated (sqlite)
	"2006-01-02 15:04.999999999",
	"2006-01-02",
}

// AsUtc returns a UTC time, interpreting legacy naive values as UTC.
//
// Persisted memory timestamps are intended to describe an absolute instant.
// Older Episode rows and imperfect LLM output may omit the timezone suffix;
// treating those values as UTC matches the extractor prompt and the fallback
// raw-event timestamps while keeping legacy stores readable. In Go a
// time.Time always carries a location, so normalization is a plain .UTC()
// conversion (which is a no-op for zero values).
func AsUtc(t time.Time) time.Time {
	return t.UTC()
}

// ParseDatetimeUTC parses an ISO 8601 timestamp and normalizes it to UTC.
// Values without a timezone suffix are interpreted as UTC.
func ParseDatetimeUTC(value string) (time.Time, error) {
	for _, layout := range datetimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &time.ParseError{Value: value, Layout: "iso8601", LayoutElem: "", Message: ": unsupported timestamp format"}
}

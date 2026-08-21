package store

import (
	"time"

	"api_distribution/internal/types"
)

// DayLayout is the calendar-date key used for a daily aggregation file
// (e.g. "stats/2026-08-20.json") and for the in-memory days map.
const DayLayout = "2006-01-02"

// DailyAgg aggregates all requests for one calendar day. It is the unit
// of persistence for statistics: one JSON file per day under the stats
// directory. Keeping stats per-date means historical volume survives
// process restarts and is not limited by the in-memory ring buffer or
// the boundaries of a single session.
type DailyAgg struct {
	Date              string                     `json:"date"` // DayLayout
	TotalRequests     int64                      `json:"totalRequests"`
	TotalInputTokens  int64                      `json:"totalInputTokens"`
	TotalOutputTokens int64                      `json:"totalOutputTokens"`
	Errors            int64                      `json:"errors"`
	LatencySumMs      int64                      `json:"latencySumMs"`
	ByModel           map[string]int64           `json:"byModel"`
	ByProvider        map[string]int64           `json:"byProvider"`
	ByClientKey       map[string]int64           `json:"byClientKey"`
	ByHour            map[int64]types.HourBucket `json:"byHour"`
}

func newDaily(date string) *DailyAgg {
	return &DailyAgg{
		Date:        date,
		ByModel:     map[string]int64{},
		ByProvider:  map[string]int64{},
		ByClientKey: map[string]int64{},
		ByHour:      map[int64]types.HourBucket{},
	}
}

// dateKey returns the DayLayout string for a unix-nano timestamp.
func dateKey(ts int64) string { return time.Unix(0, ts).Format(DayLayout) }

// add folds one proxied request into the day's aggregates.
func (d *DailyAgg) add(e types.LogEntry) {
	if d.ByModel == nil {
		d.ByModel = map[string]int64{}
	}
	if d.ByProvider == nil {
		d.ByProvider = map[string]int64{}
	}
	if d.ByClientKey == nil {
		d.ByClientKey = map[string]int64{}
	}
	if d.ByHour == nil {
		d.ByHour = map[int64]types.HourBucket{}
	}

	// Count as an error only when the HTTP status code signals a
	// failure (>=400). We deliberately ignore e.Error here: a streaming
	// response that started with a 200 OK but was cut short by the
	// client disconnecting produces scanner.Err() like "broken pipe"
	// while the *upstream* response itself succeeded — those must not
	// pollute the error rate shown on the dashboard.
	isErr := e.StatusCode >= 400
	d.TotalRequests++
	d.TotalInputTokens += int64(e.InputTokens)
	d.TotalOutputTokens += int64(e.OutputTokens)
	d.LatencySumMs += e.LatencyMs
	if isErr {
		d.Errors++
	}
	if e.Alias != "" {
		d.ByModel[e.Alias]++
	}
	if e.ProviderName != "" {
		d.ByProvider[e.ProviderName]++
	}
	if e.ClientKeyLabel != "" {
		d.ByClientKey[e.ClientKeyLabel]++
	}
	h := time.Unix(0, e.Timestamp).Truncate(time.Hour).Unix()
	a := d.ByHour[h]
	a.Count++
	if isErr {
		a.Errors++
	}
	if e.Alias != "" {
		if a.ByModel == nil {
			a.ByModel = map[string]int64{}
		}
		a.ByModel[e.Alias]++
	}
	d.ByHour[h] = a
}

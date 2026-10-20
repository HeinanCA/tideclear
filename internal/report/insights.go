package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/HeinanCA/tideclear/internal/insights"
)

const (
	staleAfter  = 24 * time.Hour
	staleAsDays = 48 * time.Hour
)

// InsightsText writes the insights table. REFRESHED is relative to now; when
// any insight is older than 24h a hint points at the console button, because
// tideclear never starts a refresh itself.
func InsightsText(w io.Writer, ins []insights.Insight, now time.Time) error {
	if len(ins) == 0 {
		_, err := fmt.Fprintln(w, "no insights found")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tTARGET\tSTATUS\tREFRESHED")
	stale := false
	for _, in := range ins {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", in.Name, dash(in.Target), in.Status, refreshed(in.LastRefresh, now))
		stale = stale || (!in.LastRefresh.IsZero() && now.Sub(in.LastRefresh) > staleAfter)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !stale {
		return nil
	}
	_, err := fmt.Fprintln(w, `hint: some insights were refreshed more than 24h ago; use the console "Refresh insights" button for current data`)
	return err
}

func refreshed(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < staleAsDays:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
}

type insightJSON struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Target          string  `json:"target"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason"`
	LastRefreshTime *string `json:"lastRefreshTime"`
}

type insightsDoc struct {
	SchemaVersion int           `json:"schemaVersion"`
	Insights      []insightJSON `json:"insights"`
}

// InsightsJSON writes the insights document (schemaVersion 1); insights is
// always an array.
func InsightsJSON(w io.Writer, ins []insights.Insight) error {
	doc := insightsDoc{SchemaVersion: schemaVersion, Insights: []insightJSON{}}
	for _, in := range ins {
		j := insightJSON{ID: in.ID, Name: in.Name, Target: in.Target, Status: string(in.Status), Reason: in.Reason}
		if !in.LastRefresh.IsZero() {
			s := in.LastRefresh.UTC().Format(time.RFC3339)
			j.LastRefreshTime = &s
		}
		doc.Insights = append(doc.Insights, j)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

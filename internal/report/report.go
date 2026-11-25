// Package report renders a comparison summary for the terminal or as JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/cansarihan/rift/internal/diff"
)

// JSON writes the summary as indented JSON.
func JSON(w io.Writer, summary diff.Summary) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

// Table writes a human-readable report: a header that tallies the comparison
// and a table of changed functions, worst first.
func Table(w io.Writer, summary diff.Summary) {
	fmt.Fprintf(w, "%s\n   -> %s\n", summary.Old, summary.New)
	fmt.Fprintf(w, "%d functions compared  ·  modified %d  added %d  removed %d\n",
		summary.Compared, summary.Modified, summary.Added, summary.Removed)
	fmt.Fprintf(w, "rift %s  ·  %d flagged  ·  likely:%d possible:%d unlikely:%d\n\n",
		summary.Band, summary.Flagged,
		summary.Counts[diff.BandLikely], summary.Counts[diff.BandPossible], summary.Counts[diff.BandUnlikely])

	if len(summary.Deltas) == 0 {
		fmt.Fprintln(w, "no differences in the compared functions")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BAND\tSCORE\tSTATUS\tFUNCTION\tEVIDENCE")
	for _, d := range summary.Deltas {
		if d.Band == diff.BandUnlikely && d.Status == diff.StatusRemoved {
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n",
			d.Band, d.Score, d.Status, truncate(d.Name, 44), strings.Join(d.Reasons, "; "))
	}
	_ = tw.Flush()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

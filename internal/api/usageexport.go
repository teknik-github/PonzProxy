package api

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/report"
)

// chartBars is how many hosts the PDF's chart shows before the rest are
// summarised. A chart with forty bars is a table with extra steps.
const chartBars = 12

// handleUsageXLSX and handleUsagePDF serve the same report the console shows,
// as files an operator can file, mail or attach to an invoice.
//
// They are built into the binary rather than pulled from a library: see the
// report package for why. Both render from usageReport, so a download can
// never disagree with the screen it was started from.
func (s *Server) handleUsageXLSX(w http.ResponseWriter, r *http.Request) {
	s.serveUsageFile(w, r, "xlsx")
}

func (s *Server) handleUsagePDF(w http.ResponseWriter, r *http.Request) {
	s.serveUsageFile(w, r, "pdf")
}

func (s *Server) serveUsageFile(w http.ResponseWriter, r *http.Request, format string) {
	from, to, err := usageWindow(r)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	data, err := s.usageReport(r, from, to)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	table := usageTable(data)

	// Rendered into a buffer first so a failure half way through becomes a
	// 500 rather than a truncated file the browser has already started
	// saving under a name that suggests it worked.
	var buf bytes.Buffer
	var contentType string
	switch format {
	case "xlsx":
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		err = report.XLSX(&buf, table)
	default:
		contentType = "application/pdf"
		err = report.PDF(&buf, table)
	}
	if err != nil {
		writeError(w, s.logger, fmt.Errorf("render the %s report: %w", format, err))
		return
	}

	name := fmt.Sprintf("ponzproxy-traffic-%s-to-%s.%s",
		from.Format("2006-01-02"), to.Format("2006-01-02"), format)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
	// A report is a snapshot of a moving figure; a cached copy would show
	// yesterday's numbers under today's filename.
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(buf.Bytes()); err != nil {
		s.logger.Debug("usage download interrupted", "format", format, "error", err)
	}
}

// usageWindow parses from and to, defaulting to the last 30 days.
func usageWindow(r *http.Request) (from, to time.Time, err error) {
	q := r.URL.Query()
	to = time.Now().UTC()
	from = to.AddDate(0, 0, -30)

	if v := q.Get("from"); v != "" {
		parsed, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return from, to, errors.Join(errBadRequest,
				errors.New("from must be an RFC3339 timestamp"))
		}
		from = parsed.UTC()
	}
	if v := q.Get("to"); v != "" {
		parsed, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return from, to, errors.Join(errBadRequest,
				errors.New("to must be an RFC3339 timestamp"))
		}
		to = parsed.UTC()
	}
	if to.Before(from) {
		return from, to, errors.Join(errBadRequest, errors.New("from must be before to"))
	}
	return from, to, nil
}

// usageTable turns the report into the shape both renderers take.
func usageTable(data usageResponse) report.Table {
	t := report.Table{
		Title: "Traffic used",
		Subtitle: fmt.Sprintf("%s to %s (%s)",
			data.From.Format("2 January 2006"),
			data.To.Format("2 January 2006"),
			humanWindow(data.To.Sub(data.From))),
		ChartTitle: "Total traffic by host",
		Generated:  time.Now().UTC(),
		Columns: []report.Column{
			{Title: "Host", Width: 28},
			{Title: "Requests", Align: report.Right, Number: true, Width: 14},
			{Title: "In (bytes)", Align: report.Right, Number: true, Width: 16},
			{Title: "Out (bytes)", Align: report.Right, Number: true, Width: 16},
			{Title: "Total (bytes)", Align: report.Right, Number: true, Width: 16},
			{Title: "Share", Align: report.Right, Width: 10},
		},
	}

	var total, totalIn, totalOut, totalReq uint64
	for _, row := range data.Rows {
		total += row.Total
		totalIn += row.BytesIn
		totalOut += row.BytesOut
		totalReq += row.Requests
	}

	for i, row := range data.Rows {
		share := 0.0
		if total > 0 {
			share = float64(row.Total) / float64(total)
		}
		t.Rows = append(t.Rows, []report.Cell{
			report.Text(row.Name),
			report.Number(fmt.Sprint(row.Requests), float64(row.Requests)),
			report.Number(fmt.Sprint(row.BytesIn), float64(row.BytesIn)),
			report.Number(fmt.Sprint(row.BytesOut), float64(row.BytesOut)),
			report.Number(fmt.Sprint(row.Total), float64(row.Total)),
			report.Text(fmt.Sprintf("%.1f%%", share*100)),
		})
		if i < chartBars {
			t.Chart = append(t.Chart, report.Bar{
				Label: row.Name,
				Value: float64(row.Total),
				Text:  humanBytes(row.Total),
			})
		}
	}

	if len(data.Rows) > 0 {
		t.Total = []report.Cell{
			report.Text("All hosts"),
			report.Number(fmt.Sprint(totalReq), float64(totalReq)),
			report.Number(fmt.Sprint(totalIn), float64(totalIn)),
			report.Number(fmt.Sprint(totalOut), float64(totalOut)),
			report.Number(fmt.Sprint(total), float64(total)),
			report.Text("100.0%"),
		}
	}

	t.Notes = []string{
		"Counted at the proxy: request and response bytes as they crossed it, headers included. " +
			"A response served from the cache still counts, because the visitor downloaded it either way. " +
			"A request refused by an access list, request inspection or a traffic limit never reached a " +
			"backend and shows only the bytes it cost to refuse.",
	}
	if data.Truncated {
		t.Notes = append(t.Notes, fmt.Sprintf(
			"History is kept for %d days, so this window covers less time than it asks for. "+
				"Raise PONZ_METRICS_RETENTION to report further back.", data.RetentionDays))
	}
	if len(data.Rows) > chartBars {
		t.Notes = append(t.Notes, fmt.Sprintf(
			"The chart shows the %d heaviest hosts; the table below lists all %d.",
			chartBars, len(data.Rows)))
	}
	return t
}

// humanWindow describes a duration the way someone would say it out loud.
func humanWindow(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// humanBytes matches the console's formatting, so a figure read on screen and
// the same figure in a PDF do not look like two different numbers.
func humanBytes(n uint64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	unit := 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	digits := 0
	if value < 10 && unit > 0 {
		digits = 1
	}
	return strings.TrimSpace(fmt.Sprintf("%.*f %s", digits, value, units[unit]))
}

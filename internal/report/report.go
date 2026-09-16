// Package report renders a table as a spreadsheet or a PDF.
//
// Both formats are written against the standard library rather than pulled in
// as dependencies. That is a deliberate trade: an .xlsx is a zip of XML and a
// PDF of a table in a standard font is a few thousand bytes of text, so the
// code here is small and readable, where the usual libraries would each add
// more to the binary than the whole proxy currently weighs. This project ships
// one static binary and has four direct dependencies; a download button is not
// worth changing that.
//
// What this does not do: styling beyond bold headers, formulas, charts,
// embedded fonts, or anything that needs text measurement. A report is a grid
// of numbers with a title.
package report

import "time"

// Align says how a column is set. Numbers read far better right-aligned, and
// it is the only typographic decision worth making here.
type Align int

const (
	Left Align = iota
	Right
)

// Column describes one column of the table.
type Column struct {
	Title string
	Align Align
	// Width is in characters, used as a hint by both renderers. Zero lets
	// the renderer pick from the title.
	Width int
	// Number marks a column whose cells are numeric, so a spreadsheet
	// stores them as numbers an operator can sum rather than as text that
	// merely looks like one.
	Number bool
}

// Cell is one value. Text is what a PDF draws and what a spreadsheet shows;
// Value is what a spreadsheet stores when the column is numeric.
//
// They are separate because the two want different things from the same
// figure: a report reads better as "47 MB", while a spreadsheet is useless
// unless the cell holds 46716739 and can be added up.
type Cell struct {
	Text  string
	Value float64
}

// Text builds a text cell.
func Text(s string) Cell { return Cell{Text: s} }

// Number builds a numeric cell, showing text and storing value.
func Number(text string, value float64) Cell { return Cell{Text: text, Value: value} }

// Bar is one entry of a chart: a label, the value that sets its length, and
// the text printed at its end. Text is carried separately for the same reason
// Cell does it — "47 MB" reads better than 46716739, but only the number can
// set a bar's length.
type Bar struct {
	Label string
	Value float64
	Text  string
}

// Table is what both renderers take.
type Table struct {
	// Title heads the document and names the spreadsheet's sheet.
	Title string
	// Subtitle is one line under it — the window the figures cover.
	Subtitle string
	Columns  []Column
	Rows     [][]Cell
	// Total is an optional summary row, set apart from the rest.
	Total []Cell
	// Chart is drawn above the table by the PDF renderer, which can draw
	// vectors natively. The spreadsheet ignores it: an .xlsx chart is a
	// large amount of XML to reproduce something the recipient can make in
	// two clicks from data they already have.
	Chart      []Bar
	ChartTitle string
	// Notes are printed after the table. This is where a caveat goes: a
	// figure someone will compare against a bill needs to say what it did
	// and did not count.
	Notes []string
	// Generated stamps the document, so two copies of the same report can
	// be told apart.
	Generated time.Time
}

// Package web owns every rendered page. Callers build a view value and hand it
// to one of the render functions, which writes finished HTML to an io.Writer —
// scrape writes that to disk, serve writes it to the response. The view types
// are presentation-only (finished strings, no scoring types), so web depends on
// nothing else in the module.
package web

import (
	"embed"
	"html/template"
	"io"
	"strings"
)

//go:embed *.gohtml
var files embed.FS

var tmpl = template.Must(template.New("web").ParseFS(files, "*.gohtml"))

// Region renders the region summary page. Area renders a per-area subpage.
// About and Error render the serve-side pages. All write to w.
func Region(w io.Writer, v RegionView) error { return tmpl.ExecuteTemplate(w, "region.gohtml", v) }
func Area(w io.Writer, v AreaView) error     { return tmpl.ExecuteTemplate(w, "area.gohtml", v) }
func About(w io.Writer, v AboutView) error   { return tmpl.ExecuteTemplate(w, "about.gohtml", v) }
func Error(w io.Writer, v ErrorView) error   { return tmpl.ExecuteTemplate(w, "error.gohtml", v) }

// ----- shared page chrome -----

const siteURL = "https://cragcast.org"

// Meta is the context the shared meta partial renders against: title,
// description, canonical URL, and their Open Graph copies for link previews.
type Meta struct {
	Title       string
	Description string
	URL         string
}

// Chrome is the header/footer context every forecast page shares.
type Chrome struct {
	Region   string
	RegionId string
	Updated  string
	Footer   FooterData
	Days     []DayHeader
}

// FooterData is the context the shared footer partial renders against.
type FooterData struct {
	Github    string // repo URL; defaults to the Cragcast repository when empty
	ShowAbout bool   // false on the about page itself
}

type DayHeader struct {
	Label string // "Today", "Wed", ...
	Date  string // "Jun 16"
}

// ----- region summary page -----

// RegionView is the summary page: one row per area, colored by the area-level
// verdict. CragsJSON carries the per-day crag breakdown the client reveals when
// a day cell is tapped.
type RegionView struct {
	Chrome
	Areas     []AreaSummary
	CragsJSON template.JS
}

type AreaSummary struct {
	Name  string
	Id    string
	Drive string
	Cells []SummaryCell // exactly one per forecast day
}

type SummaryCell struct {
	Class string   // go|warn|wet
	Icons []string // one glyph, or two (rendered with a "/" between them)
	Temp  string   // avg day-high across the area's crags, e.g. "58°"
	Day   int      // index into CragsJSON[areaId]
	Label string   // aria-label
}

// ----- per-area subpage -----

// AreaView is the per-area subpage: the full crag-by-crag forecast.
type AreaView struct {
	Chrome
	AreaId   string
	AreaName string
	Drive    string
	Crags    []CragView
}

func (v RegionView) Meta() Meta {
	return Meta{
		Title:       "Climbing conditions near " + v.Region + " — Cragcast",
		Description: "Compare climbing conditions at crags near " + v.Region + " with forecasts for rain, temperature, wind, and air quality.",
		URL:         siteURL + "/" + v.RegionId,
	}
}

func (v AreaView) Meta() Meta {
	return Meta{
		Title:       v.AreaName + " climbing conditions — Cragcast",
		Description: v.Description(),
		URL:         siteURL + "/" + v.RegionId + "/" + v.AreaId,
	}
}

// Description summarizes the area using its configured crag names.
func (v AreaView) Description() string {
	names := make([]string, len(v.Crags))
	for i, crag := range v.Crags {
		names[i] = crag.Name
	}
	var crags string
	switch len(names) {
	case 0:
		crags = v.AreaName
	case 1:
		crags = names[0]
	case 2:
		crags = strings.Join(names, " and ")
	default:
		crags = strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
	if crags != v.AreaName {
		crags += " at " + v.AreaName
	}
	return "Climbing weather for " + crags + ". Rain, wind, and air quality forecasts."
}

type CragView struct {
	Name       string
	Aspect     string
	AspectIcon string
	Cells      []Cell // exactly one per forecast day
}

// Cell is either a solid verdict or a split AM/PM pair (template branches on Split).
type Cell struct {
	Split bool
	Class string // solid: go|warn|wet
	Icon  string // solid icon id
	Temp  string // solid day-high, e.g. "58°"
	AM    Half
	PM    Half
	Label string // aria-label

	// Tap-to-expand detail, carried as compact data-* attributes and formatted
	// client-side. See windowData in scrape for the field order.
	Day    string // panel header, e.g. "Wed, Jun 18"
	AMData string
	PMData string
}

type Half struct {
	Class string
	Icon  string
}

// ----- serve-side pages -----

type AboutView struct {
	Footer FooterData
}

func (AboutView) Meta() Meta {
	return Meta{
		Title:       "About — Cragcast",
		Description: "Learn how Cragcast rates climbing conditions using weather and air quality forecasts from Open-Meteo and CAMS.",
		URL:         siteURL + "/about",
	}
}

type ErrorView struct {
	Status  int
	Message string
	Footer  FooterData
}

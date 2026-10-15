package scrape

import (
	"strings"
	"testing"

	"github.com/anton-evseev/cragcast/web"
)

func TestCellTemperatureWithoutData(t *testing.T) {
	for _, tc := range []struct {
		name string
		day  DayForecast
		want string
	}{
		{"missing", DayForecast{}, "—"},
		{"invalid measurements", DayForecast{AM: Window{TmaxF: 65}, PM: Window{TmaxF: 70}}, "—"},
		{"real zero", DayForecast{AM: Window{Valid: true}, PM: Window{Valid: true}}, "0°"},
		{"valid", DayForecast{AM: Window{Valid: true, TmaxF: 60}, PM: Window{Valid: true, TmaxF: 65}}, "65°"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cell := cells([]DayForecast{tc.day}, Profile{Aspect: "shade"}, DefaultThresholds())[0]
			if cell.Temp != tc.want {
				t.Fatalf("temperature = %q, want %q", cell.Temp, tc.want)
			}
			var html strings.Builder
			if err := web.Area(&html, web.AreaView{Crags: []web.CragView{{Cells: []web.Cell{cell}}}}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(html.String(), `<span class="temp">`+tc.want+`</span`) {
				t.Fatalf("rendered cell is missing temperature %q", tc.want)
			}
		})
	}
}

func TestGreenWeatherIcons(t *testing.T) {
	p := Profile{Aspect: "shade", DryoutInches: 1}
	th := DefaultThresholds()
	for _, tc := range []struct {
		cloud float64
		icon  string
		text  string
	}{
		{0, "i-sun", "mostly clear"},
		{20, "i-sun", "mostly clear"},
		{20.1, "i-partly-cloudy", "partly cloudy"},
		{79.9, "i-partly-cloudy", "partly cloudy"},
		{80, "i-cloud", "overcast"},
		{100, "i-cloud", "overcast"},
	} {
		w := Window{Valid: true, TmaxF: 65, CloudMean: tc.cloud}
		day := DayForecast{AM: w, PM: w}
		cell := cells([]DayForecast{day}, p, th)[0]
		if cell.Class != "go" || cell.Icon != tc.icon || !strings.Contains(cell.Label, tc.text) {
			t.Fatalf("cloud cover %g: %+v", tc.cloud, cell)
		}
		cd := cragDays([]DayForecast{day}, p, th)[0]
		icons := areaIcon(Green, nil, cd.CloudMean)
		if len(icons) != 1 || icons[0] != tc.icon {
			t.Fatalf("cloud cover %g: region icons %v", tc.cloud, icons)
		}
	}

	day := DayForecast{
		AM: Window{Valid: true, TmaxF: 65, CloudMean: 0},
		PM: Window{Valid: true, TmaxF: 65, CloudMean: 100},
	}
	if cell := cells([]DayForecast{day}, p, th)[0]; cell.Icon != "i-partly-cloudy" {
		t.Fatalf("daily cloud average ignored: %+v", cell)
	}
	day.AM.CloudMean = 100
	day.PM.WindMaxMph = 30
	cell := cells([]DayForecast{day}, p, th)[0]
	if !cell.Split || cell.AM.Icon != "i-cloud" || cell.PM.Icon != "i-wind" {
		t.Fatalf("split cell lost weather or warning icon: %+v", cell)
	}
	if !strings.Contains(cell.Label, "overcast") || !strings.Contains(cell.Label, "windy") {
		t.Fatalf("split cell label lost weather or warning: %q", cell.Label)
	}
}

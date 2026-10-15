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

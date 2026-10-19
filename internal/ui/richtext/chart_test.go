package richtext

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

// wave returns n comma-separated values of a deterministic noisy sine.
func wave(n int, scale float64) string {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = fmt.Sprintf("%.2f", scale*(50+40*math.Sin(float64(i)/15)+float64(i%7)))
	}
	return strings.Join(vals, ",")
}

var goldenCharts = []struct{ name, body string }{
	{"hbar_single", `{"type":"hbar","title":"Revenue by region","unit":"$","labels":["North America","Europe","Asia Pacific","LatAm"],"series":[{"name":"2025","values":[1250000,830000,640000,120000]}]}`},
	{"hbar_multi", `{"type":"hbar","title":"Requests","labels":["api","web","worker"],"series":[{"name":"mon","values":[1200,800,300]},{"name":"tue","values":[1500,650,null]},{"name":"wed","values":[900,1000,420]}]}`},
	{"hbar_negative", `{"type":"hbar","title":"Net change","unit":"%","labels":["Alpha","Beta","Gamma","Delta"],"series":[{"name":"Q1","values":[12.5,-4.2,7,-9.8]},{"name":"Q2","values":[3,2,-1.5,4]}]}`},
	{"hbar_units", `{"type":"hbar","unit":"ms","labels":["p50","p90","p99","max"],"series":[{"values":[12.4,48,230,1850]}]}`},
	{"bar_single", `{"type":"bar","title":"Monthly signups","labels":["Jan","Feb","Mar","Apr","May","Jun"],"series":[{"name":"signups","values":[120,340,280,510,460,620]}]}`},
	{"bar_multi", `{"type":"bar","title":"Builds","labels":["mon","tue","wed","thu","fri"],"series":[{"name":"passed","values":[42,38,51,47,30]},{"name":"failed","values":[3,7,2,5,9]}]}`},
	{"bar_negative", `{"type":"bar","title":"P&L","unit":"$","labels":["Q1","Q2","Q3","Q4"],"series":[{"name":"A","values":[1200,-800,300,-150]},{"name":"B","values":[400,600,-900,1000]}]}`},
	{"bar_units", `{"type":"bar","title":"Storage","unit":"GB","labels":["db","logs","media","backups"],"series":[{"values":[120.5,48.25,980,2400]}]}`},
	{"bar_downsampled", `{"type":"bar","title":"Daily active users","series":[{"name":"dau","values":[` + wave(260, 1000) + `]}]}`},
	{"line_single", `{"type":"line","title":"CPU","unit":"%","labels":["00:00","04:00","08:00","12:00","16:00","20:00","24:00"],"series":[{"name":"cpu","values":[12,18,64,88,71,40,15]}]}`},
	{"line_multi", `{"type":"line","title":"Latency","unit":"ms","labels":["00:00","04:00","08:00","12:00","16:00","20:00","24:00"],"series":[{"name":"p50","values":[12,14,22,30,26,18,13]},{"name":"p99","values":[40,45,80,120,95,60,42]}]}`},
	{"line_negative", `{"type":"line","title":"Temperature","unit":"°C","labels":["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"],"series":[{"name":"Oslo","values":[-4.3,-4,-0.2,4.5,10.8,15.2,16.4,15.2,10.8,6.3,0.7,-3.1]},{"name":"Rome","values":[7.5,8.2,10.2,12.6,17.2,21.1,24.1,24.5,20.8,16.4,11.4,8.4]}]}`},
	{"line_downsampled", `{"type":"line","title":"Throughput","unit":"$","series":[{"name":"rev","values":[` + wave(400, 25) + `]},{"name":"cost","values":[` + wave(400, 10) + `]}]}`},
	{"line_gaps", `{"type":"line","series":[{"values":[1,2,null,4,5,3,null,null,2,1]}]}`},
	{"spark_single", `{"type":"spark","series":[{"values":[3,5,2,8,6,9,4,7,10,6,5,8]}]}`},
	{"spark_multi", `{"type":"spark","title":"Prices","unit":"$","series":[{"name":"AAPL","values":[180,182,179,185,190,188,195,193,198]},{"name":"MSFT","values":[410,405,415,420,418,430,428,435,440]},{"name":"flat","values":[5,5,5,5]}]}`},
	{"spark_downsampled", `{"type":"spark","title":"Error rate","unit":"%","series":[{"name":"api","values":[` + wave(300, 0.01) + `]}]}`},
	{"spark_negative", `{"type":"spark","series":[{"name":"pnl","values":[-120,-40,10,85,-30,-220,60,140]}]}`},
	{"too_many_series", `{"type":"line","series":[` + strings.Repeat(`{"values":[1,3,2]},`, 10) + `{"values":[2,1,3]}]}`},
}

func TestRenderChartGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenCharts {
		for _, width := range []int{40, 80} {
			for _, th := range themes {
				if width == 40 && th.name == "light" {
					continue
				}
				name := fmt.Sprintf("chart_%s_%d_%s", tc.name, width, th.name)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					out, err := RenderChart(tc.body, width, th.th)
					if err != nil {
						t.Fatal(err)
					}
					assertLayout(t, out, width)
					plain := th.th
					plain.Plain = true
					p, _ := RenderChart(tc.body, width, plain)
					assertPlain(t, out, p)
					golden(t, name, out)
				})
			}
		}
	}
}

func TestRenderChartWidths(t *testing.T) {
	t.Parallel()
	for _, tc := range goldenCharts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for width := minChartWidth; width <= 130; width++ {
				out, err := RenderChart(tc.body, width, DarkTheme())
				if err != nil {
					t.Fatalf("width %d: %v", width, err)
				}
				assertLayout(t, out, min(width, maxChartWidth))
			}
		})
	}
}

func TestRenderChartNotes(t *testing.T) {
	t.Parallel()
	tests := []struct{ body, want string }{
		{goldenCharts[18].body, "+3 series not shown"},
		{goldenCharts[8].body, "downsampled"},
		{`{"type":"bar","series":[{"values":[` + wave(201, 1) + `]}]}`, "downsampled"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			out, err := RenderChart(tt.body, 80, Theme{Plain: true})
			if err != nil {
				t.Fatal(err)
			}
			last := out[strings.LastIndexByte(out, '\n')+1:]
			if !strings.Contains(last, tt.want) {
				t.Errorf("last line %q lacks %q", last, tt.want)
			}
		})
	}
}

func TestRenderChartErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		body  string
		width int
		want  string
	}{
		{`{"type":"bar","series":[{"values":[1]}]}`, 19, "too narrow"},
		{`{"type":"bar",`, 80, "invalid JSON at byte 14"},
		{`[1,2]`, 80, "invalid JSON: want an object"},
		{`{"type":"bar" "x"}`, 80, "invalid JSON at byte 15"},
		{`{"type":"bar","series":[{"values":["a"]}]}`, 80, "invalid JSON: series.0.values.0 must be float64"},
		{`{"series":[{"values":[1]}]}`, 80, "missing type"},
		{`{"type":"pie","series":[{"values":[1]}]}`, 80, `unknown type "pie" (want bar, hbar, line or spark)`},
		{`{"type":"line"}`, 80, "no series"},
		{`{"type":"line","series":[{"values":[null]},{"values":[]}]}`, 80, "no values"},
		{`{"type":"hbar","labels":["a"],"series":[{"values":[123456789012345678901234567890]}]}`, 20, "too narrow"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			_, err := RenderChart(tt.body, tt.width, DarkTheme())
			if err == nil || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestErrorLine(t *testing.T) {
	t.Parallel()
	if got := ErrorLine(errors.New("no series"), Theme{Plain: true}); got != "chart: no series" {
		t.Errorf("ErrorLine = %q", got)
	}
	if got := ErrorLine(errors.New("x"), DarkTheme()); !strings.Contains(got, "\x1b[") || !strings.Contains(got, "chart: x") {
		t.Errorf("ErrorLine not styled: %q", got)
	}
}

func TestNiceTicks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		lo, hi float64
		want   []float64
	}{
		{0, 100, []float64{0, 20, 40, 60, 80, 100}},
		{12, 88, []float64{0, 20, 40, 60, 80, 100}},
		{-4.3, 24.5, []float64{-10, 0, 10, 20, 30}},
		{0.001, 0.0042, []float64{0.001, 0.002, 0.003, 0.004, 0.005}},
		{7, 7, []float64{6, 6.5, 7, 7.5, 8}},
		{0, 0, []float64{-1, -0.5, 0, 0.5, 1}},
		{1200, 98000, []float64{0, 20000, 40000, 60000, 80000, 100000}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.lo, tt.hi), func(t *testing.T) {
			t.Parallel()
			got, _ := niceTicks(tt.lo, tt.hi, 5)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("niceTicks(%v, %v) = %v, want %v", tt.lo, tt.hi, got, tt.want)
			}
			if got[0] > tt.lo || got[len(got)-1] < tt.hi {
				t.Errorf("ticks %v do not cover [%v, %v]", got, tt.lo, tt.hi)
			}
		})
	}
}

func TestTickLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		lo, hi float64
		unit   string
		want   string
	}{
		{0, 100, "%", "[0% 20% 40% 60% 80% 100%]"},
		{1200, 98000, "$", "[$0 $20k $40k $60k $80k $100k]"},
		{0, 2500, "", "[0 1k 2k 3k]"},
		{0, 1, "", "[0 0.2 0.4 0.6 0.8 1]"},
		{-4.3, 24.5, "°C", "[-10°C 0°C 10°C 20°C 30°C]"},
		{0.001, 0.0042, "", "[0.001 0.002 0.003 0.004 0.005]"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			ticks, step := niceTicks(tt.lo, tt.hi, 5)
			if got := fmt.Sprint(tickLabels(ticks, step, tt.unit)); got != tt.want {
				t.Errorf("tickLabels = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v    float64
		unit string
		want string
	}{
		{0, "", "0"},
		{7, "", "7"},
		{12.345, "", "12.35"},
		{123.45, "", "123.5"},
		{999.96, "", "1k"},
		{1234, "", "1.2k"},
		{99_950, "", "100k"},
		{123_456, "", "123k"},
		{999_999, "", "1M"},
		{1_250_000, "$", "$1.2M"},
		{-830_000, "€", "-€830k"},
		{4.2e9, "£", "£4.2B"},
		{7.5e12, "¥", "¥7.5T"},
		{12.5, "%", "12.5%"},
		{230, "ms", "230ms"},
		{3, "req/s", "3 req/s"},
		{0.0042, "", "0.0042"},
		{-0.0001, "", "-0.0001"},
		{math.NaN(), "$", ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := formatValue(tt.v, tt.unit); got != tt.want {
				t.Errorf("formatValue(%v, %q) = %q, want %q", tt.v, tt.unit, got, tt.want)
			}
		})
	}
}

func TestDownsample(t *testing.T) {
	t.Parallel()
	nan := math.NaN()
	tests := []struct {
		in   []float64
		n    int
		want string
	}{
		{[]float64{1, 2, 3, 4}, 2, "[1.5 3.5]"},
		{[]float64{1, 2, 3, 4, 5, 6}, 3, "[1.5 3.5 5.5]"},
		{[]float64{1, 2, 3, 4, 5}, 2, "[1.5 4]"},
		{[]float64{1, nan, 3, nan, nan, nan}, 3, "[1 3 NaN]"},
		{[]float64{1, 2}, 5, "[1 2]"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := fmt.Sprint(bucketAvg(tt.in, tt.n)); got != tt.want {
				t.Errorf("bucketAvg(%v, %d) = %s, want %s", tt.in, tt.n, got, tt.want)
			}
		})
	}
	t.Run("labels and note", func(t *testing.T) {
		t.Parallel()
		ch, err := parseChart(`{"type":"line","labels":["a","b","c","d"],"series":[{"values":[1,2,3,4]}]}`)
		if err != nil {
			t.Fatal(err)
		}
		ch.downsample(2)
		ch.downsample(1)
		if fmt.Sprint(ch.labels) != "[a]" || fmt.Sprint(ch.notes) != "[downsampled]" {
			t.Errorf("labels %v notes %v", ch.labels, ch.notes)
		}
	})
	t.Run("max points", func(t *testing.T) {
		t.Parallel()
		ch, err := parseChart(`{"type":"line","series":[{"values":[` + wave(450, 1) + `]}]}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(ch.labels) != maxPoints || len(ch.series[0].values) != maxPoints {
			t.Errorf("got %d points, want %d", len(ch.labels), maxPoints)
		}
	})
}

func TestLoadNormalizes(t *testing.T) {
	t.Parallel()
	ch, err := parseChart(`{"type":"bar","labels":["x"],"series":[{"values":[1,2,3]},{"name":" b ","values":[4]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ch.labels) != "[x 2 3]" {
		t.Errorf("labels = %v", ch.labels)
	}
	if ch.series[0].name != "series 1" || ch.series[1].name != "b" {
		t.Errorf("names = %q, %q", ch.series[0].name, ch.series[1].name)
	}
	if v := ch.series[1].values; len(v) != 3 || !math.IsNaN(v[2]) {
		t.Errorf("padding = %v", v)
	}
}

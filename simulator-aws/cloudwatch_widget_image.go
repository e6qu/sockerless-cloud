package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// GetMetricWidgetImage draws the graph the MetricWidget describes, as the
// "GetMetricWidgetImage: Metric Widget Structure and Syntax" reference lays
// the document out: the metrics array (raw metrics with "." repeating the
// previous metric's entry, and expressions), period, stat, start, end,
// title, legend, theme, timezone, view, yAxis and horizontal annotations.

type cwWidgetMetricOptions struct {
	Expression string `json:"expression"`
	Label      string `json:"label"`
	ID         string `json:"id"`
	Color      string `json:"color"`
	Period     int32  `json:"period"`
	Stat       string `json:"stat"`
	Visible    *bool  `json:"visible"`
	YAxis      string `json:"yAxis"`
	Region     string `json:"region"`
	AccountID  string `json:"accountId"`
}

type cwWidgetAxis struct {
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	Label     string   `json:"label"`
	ShowUnits *bool    `json:"showUnits"`
}

type cwWidget struct {
	Metrics     [][]json.RawMessage `json:"metrics"`
	Annotations struct {
		Horizontal []json.RawMessage `json:"horizontal"`
		Vertical   []json.RawMessage `json:"vertical"`
	} `json:"annotations"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Width    *int   `json:"width"`
	Height   *int   `json:"height"`
	Period   int32  `json:"period"`
	Stat     string `json:"stat"`
	Title    string `json:"title"`
	Theme    string `json:"theme"`
	Timezone string `json:"timezone"`
	View     string `json:"view"`
	Stacked  bool   `json:"stacked"`
	Region   string `json:"region"`
	Legend   struct {
		Position string `json:"position"`
	} `json:"legend"`
	YAxis struct {
		Left  cwWidgetAxis `json:"left"`
		Right cwWidgetAxis `json:"right"`
	} `json:"yAxis"`
}

type cwWidgetAnnotation struct {
	Value   json.RawMessage `json:"value"`
	Label   string          `json:"label"`
	Color   string          `json:"color"`
	Fill    string          `json:"fill"`
	Visible *bool           `json:"visible"`
}

// cwWidgetPalette is the default sequence CloudWatch colors graph lines with.
var cwWidgetPalette = []string{"#1f77b4", "#ff7f0e", "#2ca02c", "#d62728", "#9467bd", "#8c564b", "#e377c2", "#7f7f7f", "#bcbd22", "#17becf"}

type cwWidgetLine struct {
	label  string
	color  color.RGBA
	series cwMetricSeries
	band   *cwMetricSeries
}

type cwWidgetProblem struct{ msg string }

func (p *cwWidgetProblem) Error() string { return p.msg }

func cwWidgetErrorf(format string, args ...any) error {
	return &cwWidgetProblem{msg: fmt.Sprintf(format, args...)}
}

var cwWidgetDuration = regexp.MustCompile(`^-P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// A widget time is absolute (2018-04-25T12:00:00.000Z) or an ISO 8601
// duration before now (-PT3H).
func cwWidgetTime(value string, def string, now time.Time) (time.Time, error) {
	if value == "" {
		value = def
	}
	if m := cwWidgetDuration.FindStringSubmatch(value); m != nil {
		n := func(i int) time.Duration {
			v, _ := strconv.Atoi(m[i])
			return time.Duration(v)
		}
		d := n(1)*7*24*time.Hour + n(2)*24*time.Hour + n(3)*time.Hour + n(4)*time.Minute + n(5)*time.Second
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, cwWidgetErrorf("The time %q is neither an absolute time nor a relative duration such as -PT3H.", value)
}

func cwWidgetZone(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	if len(tz) != 5 || (tz[0] != '+' && tz[0] != '-') {
		return nil, cwWidgetErrorf("The timezone %q is not + or - followed by four digits.", tz)
	}
	h, errH := strconv.Atoi(tz[1:3])
	m, errM := strconv.Atoi(tz[3:5])
	if errH != nil || errM != nil {
		return nil, cwWidgetErrorf("The timezone %q is not + or - followed by four digits.", tz)
	}
	offset := h*3600 + m*60
	if tz[0] == '-' {
		offset = -offset
	}
	return time.FixedZone(tz, offset), nil
}

func cwWidgetColor(hex string) (color.RGBA, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}

// cwWidgetQueries turns the metrics array into MetricDataQueries, with the
// options of each entry by query Id.
func cwWidgetQueries(w cwWidget) ([]CWMetricDataQuery, map[string]cwWidgetMetricOptions, error) {
	if len(w.Metrics) == 0 || len(w.Metrics) > 100 {
		return nil, nil, cwWidgetErrorf("The metrics array must hold 1 to 100 metrics and expressions.")
	}
	period := w.Period
	if period == 0 {
		period = 300
	}
	stat := w.Stat
	if stat == "" {
		stat = "Average"
	}
	used := map[string]bool{}
	for _, entry := range w.Metrics {
		if len(entry) > 0 {
			var opts cwWidgetMetricOptions
			if json.Unmarshal(entry[len(entry)-1], &opts) == nil && opts.ID != "" {
				used[opts.ID] = true
			}
		}
	}
	var previous []string
	var queries []CWMetricDataQuery
	options := map[string]cwWidgetMetricOptions{}
	next := 0
	for i, entry := range w.Metrics {
		var opts cwWidgetMetricOptions
		var fields []string
		for j, raw := range entry {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				if s == "." && j < len(previous) {
					s = previous[j]
				}
				fields = append(fields, s)
				continue
			}
			if j != len(entry)-1 || json.Unmarshal(raw, &opts) != nil {
				return nil, nil, cwWidgetErrorf("Entry %d of the metrics array is not a list of strings with an optional options object.", i)
			}
		}
		id := opts.ID
		if id == "" {
			for {
				next++
				id = "m" + strconv.Itoa(next)
				if !used[id] {
					break
				}
			}
		}
		used[id] = true
		q := CWMetricDataQuery{Id: id, Label: opts.Label}
		if opts.Visible != nil && !*opts.Visible {
			no := false
			q.ReturnData = &no
		}
		switch {
		case opts.Expression != "":
			if len(fields) > 0 {
				return nil, nil, cwWidgetErrorf("Entry %d of the metrics array holds both a metric and an expression.", i)
			}
			q.Expression = opts.Expression
		case len(fields) >= 2 && len(fields)%2 == 0:
			ms := &CWMetricStat{Metric: CWMetricRef{Namespace: fields[0], MetricName: fields[1]}, Period: period, Stat: stat}
			for k := 2; k+1 < len(fields); k += 2 {
				ms.Metric.Dimensions = append(ms.Metric.Dimensions, CWDimension{Name: fields[k], Value: fields[k+1]})
			}
			if opts.Period > 0 {
				ms.Period = opts.Period
			}
			if opts.Stat != "" {
				ms.Stat = opts.Stat
			}
			q.MetricStat = ms
			previous = fields
		default:
			return nil, nil, cwWidgetErrorf("Entry %d of the metrics array needs a namespace, a metric name and dimension name and value pairs.", i)
		}
		queries = append(queries, q)
		options[id] = opts
	}
	return queries, options, nil
}

type cwWidgetCanvas struct {
	img  *image.RGBA
	face font.Face
}

func (c cwWidgetCanvas) text(x, y int, s string, col color.Color) {
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: c.face, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func (c cwWidgetCanvas) textWidth(s string) int {
	return font.MeasureString(c.face, s).Round()
}

func (c cwWidgetCanvas) fill(r image.Rectangle, col color.Color) {
	draw.Draw(c.img, r.Intersect(c.img.Bounds()), image.NewUniform(col), image.Point{}, draw.Over)
}

// line draws a segment `width` pixels thick.
func (c cwWidgetCanvas) line(x0, y0, x1, y1, width int, col color.RGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		for ox := 0; ox < width; ox++ {
			for oy := 0; oy < width; oy++ {
				if (image.Point{X: x0 + ox, Y: y0 + oy}).In(c.img.Bounds()) {
					c.img.SetRGBA(x0+ox, y0+oy, col)
				}
			}
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func cwRenderMetricWidget(raw string, now time.Time) ([]byte, error) {
	var w cwWidget
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return nil, cwWidgetErrorf("MetricWidget is not valid JSON.")
	}
	width, height := 600, 400
	if w.Width != nil {
		width = *w.Width
	}
	if w.Height != nil {
		height = *w.Height
	}
	if width < 1 || width > 2000 || height < 1 || height > 2000 {
		return nil, cwWidgetErrorf("The width and height must each be between 1 and 2000 pixels.")
	}
	if w.Region != "" && w.Region != awsRegion() {
		return nil, cwWidgetErrorf("The region %s is not the local Region %s.", w.Region, awsRegion())
	}
	start, err := cwWidgetTime(w.Start, "-PT3H", now)
	if err != nil {
		return nil, err
	}
	end, err := cwWidgetTime(w.End, "-PT0H", now)
	if err != nil {
		return nil, err
	}
	if !end.After(start) {
		return nil, cwWidgetErrorf("The start of the graph must be before its end.")
	}
	zone, err := cwWidgetZone(w.Timezone)
	if err != nil {
		return nil, err
	}
	queries, options, err := cwWidgetQueries(w)
	if err != nil {
		return nil, err
	}
	results, err := cwEvaluateMetricQueries(queries, start.Unix(), end.Unix())
	if err != nil {
		return nil, cwWidgetErrorf("%s", err.Error())
	}

	var lines []cwWidgetLine
	bandSeen := map[string]int{}
	for _, res := range results {
		opts := options[res.Id]
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(queriesExpression(queries, res.Id))), "ANOMALY_DETECTION_BAND") {
			if idx, ok := bandSeen[res.Id]; ok {
				lower := res.Series
				lines[idx].band = &lower
				continue
			}
			bandSeen[res.Id] = len(lines)
			col, ok := cwWidgetColor(opts.Color)
			if !ok {
				col = color.RGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff}
			}
			label := opts.Label
			if label == "" {
				label = strings.TrimSuffix(res.Label, " (upper)") + " expected range"
			}
			lines = append(lines, cwWidgetLine{label: label, color: col, series: res.Series})
			continue
		}
		col, ok := cwWidgetColor(opts.Color)
		if !ok {
			col, _ = cwWidgetColor(cwWidgetPalette[len(lines)%len(cwWidgetPalette)])
		}
		lines = append(lines, cwWidgetLine{label: res.Label, color: col, series: res.Series})
	}

	dark := w.Theme == "dark"
	bg, fg, grid := color.RGBA{0xff, 0xff, 0xff, 0xff}, color.RGBA{0x44, 0x44, 0x44, 0xff}, color.RGBA{0xe6, 0xe6, 0xe6, 0xff}
	if dark {
		bg, fg, grid = color.RGBA{0x23, 0x2f, 0x3e, 0xff}, color.RGBA{0xd5, 0xdb, 0xdb, 0xff}, color.RGBA{0x41, 0x4d, 0x5c, 0xff}
	}
	c := cwWidgetCanvas{img: image.NewRGBA(image.Rect(0, 0, width, height)), face: basicfont.Face7x13}
	c.fill(c.img.Bounds(), bg)

	top := 8
	if w.Title != "" {
		c.text(8, 18, w.Title, fg)
		top = 28
	}
	legendRows := 0
	legend := w.Legend.Position
	if legend == "" {
		legend = "bottom"
	}
	if legend == "bottom" && len(lines) > 0 {
		legendRows = 1
		x := 8
		for _, l := range lines {
			if x+c.textWidth(l.label)+24 > width-8 && x > 8 {
				legendRows++
				x = 8
			}
			x += c.textWidth(l.label) + 24
		}
	}
	plot := image.Rect(56, top+6, width-12, height-24-legendRows*16)
	legendX := 0
	if legend == "right" && len(lines) > 0 {
		longest := 0
		for _, l := range lines {
			longest = max(longest, c.textWidth(l.label))
		}
		legendX = width - longest - 28
		plot.Max.X = legendX - 8
	}

	lo, hi := math.Inf(1), math.Inf(-1)
	for _, l := range lines {
		for _, v := range l.series.Points {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
		if l.band != nil {
			for _, v := range l.band.Points {
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
		}
	}
	horizontal := cwWidgetHorizontalAnnotations(w.Annotations.Horizontal)
	for _, a := range horizontal {
		lo, hi = math.Min(lo, a.value), math.Max(hi, a.value)
		if a.to != nil {
			lo, hi = math.Min(lo, *a.to), math.Max(hi, *a.to)
		}
	}
	if math.IsInf(lo, 1) {
		lo, hi = 0, 1
	}
	if w.YAxis.Left.Min != nil {
		lo = *w.YAxis.Left.Min
	}
	if w.YAxis.Left.Max != nil {
		hi = *w.YAxis.Left.Max
	}
	if hi <= lo {
		hi = lo + 1
	}

	if plot.Dx() > 4 && plot.Dy() > 4 {
		xOf := func(ts int64) int {
			return plot.Min.X + int(float64(plot.Dx())*float64(ts-start.Unix())/float64(end.Unix()-start.Unix()))
		}
		yOf := func(v float64) int {
			return plot.Max.Y - int(float64(plot.Dy())*(v-lo)/(hi-lo))
		}
		for i := 0; i <= 4; i++ {
			v := lo + (hi-lo)*float64(i)/4
			y := yOf(v)
			c.fill(image.Rect(plot.Min.X, y, plot.Max.X, y+1), grid)
			label := strconv.FormatFloat(v, 'g', 4, 64)
			c.text(plot.Min.X-6-c.textWidth(label), y+4, label, fg)
		}
		for i := 0; i <= 4; i++ {
			ts := start.Unix() + (end.Unix()-start.Unix())*int64(i)/4
			x := xOf(ts)
			label := time.Unix(ts, 0).In(zone).Format("15:04")
			if end.Sub(start) > 48*time.Hour {
				label = time.Unix(ts, 0).In(zone).Format("01/02")
			}
			c.text(x-c.textWidth(label)/2, plot.Max.Y+16, label, fg)
		}
		c.fill(image.Rect(plot.Min.X, plot.Max.Y, plot.Max.X, plot.Max.Y+1), fg)
		c.fill(image.Rect(plot.Min.X, plot.Min.Y, plot.Min.X+1, plot.Max.Y), fg)
		for _, a := range horizontal {
			if !a.visible {
				continue
			}
			y := yOf(a.value)
			shade := a.color
			shade.A = 0x40
			switch {
			case a.to != nil:
				y2 := yOf(*a.to)
				c.fill(image.Rect(plot.Min.X, min(y, y2), plot.Max.X, max(y, y2)), shade)
			case a.fill == "above":
				c.fill(image.Rect(plot.Min.X, plot.Min.Y, plot.Max.X, y), shade)
			case a.fill == "below":
				c.fill(image.Rect(plot.Min.X, y, plot.Max.X, plot.Max.Y), shade)
			}
			c.line(plot.Min.X, y, plot.Max.X-1, y, 1, a.color)
			if a.label != "" {
				c.text(plot.Max.X-c.textWidth(a.label)-4, y-3, a.label, a.color)
			}
		}
		switch w.View {
		case "bar", "pie":
			cwWidgetLatestBars(c, plot, lines, yOf)
		default:
			for _, l := range lines {
				times := l.series.times(false)
				if l.band != nil {
					shade := l.color
					shade.A = 0x50
					for _, t := range times {
						upper, okU := l.series.Points[t]
						lower, okL := l.band.Points[t]
						if okU && okL {
							x := xOf(t)
							c.fill(image.Rect(x, yOf(upper), x+max(2, plot.Dx()/max(len(times), 1)), yOf(lower)+1), shade)
						}
					}
					continue
				}
				for i, t := range times {
					x, y := xOf(t), yOf(l.series.Points[t])
					if i == 0 {
						c.fill(image.Rect(x-1, y-1, x+2, y+2), l.color)
						continue
					}
					prev := times[i-1]
					c.line(xOf(prev), yOf(l.series.Points[prev]), x, y, 2, l.color)
				}
			}
		}
	}

	switch legend {
	case "bottom":
		x, y := 8, height-8-(legendRows-1)*16
		for _, l := range lines {
			if x+c.textWidth(l.label)+24 > width-8 && x > 8 {
				x, y = 8, y+16
			}
			c.fill(image.Rect(x, y-9, x+12, y+1), l.color)
			c.text(x+16, y, l.label, fg)
			x += c.textWidth(l.label) + 24
		}
	case "right":
		for i, l := range lines {
			y := plot.Min.Y + 12 + i*16
			c.fill(image.Rect(legendX, y-9, legendX+12, y+1), l.color)
			c.text(legendX+16, y, l.label, fg)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func queriesExpression(queries []CWMetricDataQuery, id string) string {
	for _, q := range queries {
		if q.Id == id {
			return q.Expression
		}
	}
	return ""
}

// The bar and pie views draw each series' latest value as one bar.
func cwWidgetLatestBars(c cwWidgetCanvas, plot image.Rectangle, lines []cwWidgetLine, yOf func(float64) int) {
	if len(lines) == 0 {
		return
	}
	slot := plot.Dx() / len(lines)
	for i, l := range lines {
		times := l.series.times(true)
		if len(times) == 0 {
			continue
		}
		x := plot.Min.X + i*slot + slot/4
		c.fill(image.Rect(x, yOf(l.series.Points[times[0]]), x+slot/2, plot.Max.Y), l.color)
	}
}

type cwWidgetHorizontal struct {
	value   float64
	to      *float64
	label   string
	color   color.RGBA
	fill    string
	visible bool
}

func cwWidgetHorizontalAnnotations(raw []json.RawMessage) []cwWidgetHorizontal {
	var out []cwWidgetHorizontal
	for i, item := range raw {
		var single cwWidgetAnnotation
		var band []cwWidgetAnnotation
		var h cwWidgetHorizontal
		switch {
		case json.Unmarshal(item, &band) == nil && len(band) == 2:
			single = band[0]
			if v, err := strconv.ParseFloat(strings.Trim(string(band[1].Value), `"`), 64); err == nil {
				h.to = &v
			}
		case json.Unmarshal(item, &single) == nil:
		default:
			continue
		}
		v, err := strconv.ParseFloat(strings.Trim(string(single.Value), `"`), 64)
		if err != nil {
			continue
		}
		h.value, h.label, h.fill = v, single.Label, single.Fill
		h.visible = single.Visible == nil || *single.Visible
		col, ok := cwWidgetColor(single.Color)
		if !ok {
			col, _ = cwWidgetColor(cwWidgetPalette[(i+3)%len(cwWidgetPalette)])
		}
		h.color = col
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].value < out[j].value })
	return out
}

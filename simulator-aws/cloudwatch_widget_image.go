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
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// GetMetricWidgetImage draws the graph the MetricWidget describes, as the
// "GetMetricWidgetImage: Metric Widget Structure and Syntax" reference lays
// the document out: the metrics array (raw metrics with "." repeating the
// previous metric's entry, and expressions, each on the left or right
// y-axis), period, stat, start, end, title, legend, theme, timezone, view
// (timeSeries, bar or pie), stacked, liveData, yAxis and the horizontal and
// vertical annotations.

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
	LiveData bool   `json:"liveData"`
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
	YAxis   string          `json:"yAxis"`
}

// cwWidgetPalette is the default sequence CloudWatch colors graph lines with.
var cwWidgetPalette = []string{"#1f77b4", "#ff7f0e", "#2ca02c", "#d62728", "#9467bd", "#8c564b", "#e377c2", "#7f7f7f", "#bcbd22", "#17becf"}

// cwWidgetLine is one legend entry; a band's line is its upper edge, with
// band its lower edge.
type cwWidgetLine struct {
	label  string
	color  color.RGBA
	series cwMetricSeries
	band   *cwMetricSeries
	right  bool
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

// cwWidgetTranslucent is col at alpha, unpremultiplied so draw blends it.
func cwWidgetTranslucent(col color.RGBA, alpha uint8) color.NRGBA {
	return color.NRGBA{R: col.R, G: col.G, B: col.B, A: alpha}
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

// cwWidgetAxisScale maps values onto the plot's height for one y-axis.
type cwWidgetAxisScale struct {
	lo, hi float64
	used   bool
}

func (a *cwWidgetAxisScale) include(v float64) {
	if !a.used {
		a.lo, a.hi, a.used = v, v, true
		return
	}
	a.lo, a.hi = math.Min(a.lo, v), math.Max(a.hi, v)
}

func (a *cwWidgetAxisScale) settle(axis cwWidgetAxis) {
	if !a.used {
		a.lo, a.hi = 0, 1
	}
	if axis.Min != nil {
		a.lo = *axis.Min
	}
	if axis.Max != nil {
		a.hi = *axis.Max
	}
	if a.hi <= a.lo {
		a.hi = a.lo + 1
	}
}

// cwWidgetAxisCaption is an axis' label, followed by the unit its metrics
// share unless showUnits is false.
func cwWidgetAxisCaption(axis cwWidgetAxis, lines []cwWidgetLine, right bool) string {
	caption := axis.Label
	if axis.ShowUnits != nil && !*axis.ShowUnits {
		return caption
	}
	unit := ""
	for _, l := range lines {
		if l.right != right {
			continue
		}
		if unit == "" {
			unit = l.series.Unit
		} else if l.series.Unit != unit {
			return caption
		}
	}
	if unit == "" || unit == "None" {
		return caption
	}
	if caption == "" {
		return unit
	}
	return caption + " (" + unit + ")"
}

// cwWidgetLive drops the datapoints live data would show: without liveData a
// period appears one minute after it ends.
func cwWidgetLive(s cwMetricSeries, now time.Time) cwMetricSeries {
	out := s
	out.Points = map[int64]float64{}
	for t, v := range s.Points {
		if t+int64(s.Period)+60 <= now.Unix() {
			out.Points[t] = v
		}
	}
	return out
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
	switch w.View {
	case "", "timeSeries", "bar", "pie":
	default:
		return nil, cwWidgetErrorf("The view must be timeSeries, bar or pie.")
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
	results, err := cwEvaluateMetricQueries(cwMathOptions{now: now}, queries, start.Unix(), end.Unix())
	if err != nil {
		return nil, cwWidgetErrorf("%s", err.Error())
	}
	horizontal, err := cwWidgetHorizontalAnnotations(w.Annotations.Horizontal)
	if err != nil {
		return nil, err
	}
	vertical, err := cwWidgetVerticalAnnotations(w.Annotations.Vertical)
	if err != nil {
		return nil, err
	}

	var lines []cwWidgetLine
	lowerEdges := map[string]cwMetricSeries{}
	for _, res := range results {
		opts := options[res.Id]
		series := res.Series
		if !w.LiveData {
			series = cwWidgetLive(series, now)
		}
		right := opts.YAxis == "right"
		switch series.Edge {
		case "lower":
			lowerEdges[res.Id] = series
			continue
		case "upper":
			col, ok := cwWidgetColor(opts.Color)
			if !ok {
				col = color.RGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff}
			}
			label := opts.Label
			if label == "" {
				label = series.Label + " expected range"
			}
			lower := lowerEdges[res.Id]
			lines = append(lines, cwWidgetLine{label: label, color: col, series: series, band: &lower, right: right})
			continue
		}
		col, ok := cwWidgetColor(opts.Color)
		if !ok {
			col, _ = cwWidgetColor(cwWidgetPalette[len(lines)%len(cwWidgetPalette)])
		}
		lines = append(lines, cwWidgetLine{label: res.Label, color: col, series: series, right: right})
	}

	stacked := map[int]map[int64][2]float64{}
	if w.Stacked && (w.View == "" || w.View == "timeSeries") {
		base := map[bool]map[int64]float64{false: {}, true: {}}
		for i, l := range lines {
			if l.band != nil {
				continue
			}
			spans := map[int64][2]float64{}
			for t, v := range l.series.Points {
				b := base[l.right][t]
				spans[t] = [2]float64{b, b + v}
				base[l.right][t] = b + v
			}
			stacked[i] = spans
		}
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

	scales := map[bool]*cwWidgetAxisScale{false: {}, true: {}}
	for i, l := range lines {
		if spans, ok := stacked[i]; ok {
			for _, span := range spans {
				scales[l.right].include(span[0])
				scales[l.right].include(span[1])
			}
			continue
		}
		for _, v := range l.series.Points {
			scales[l.right].include(v)
		}
		if l.band != nil {
			for _, v := range l.band.Points {
				scales[l.right].include(v)
			}
		}
	}
	for _, a := range horizontal {
		if !a.visible {
			continue
		}
		scales[a.right].include(a.value)
		if a.to != nil {
			scales[a.right].include(*a.to)
		}
	}
	hasRight := scales[true].used
	scales[false].settle(w.YAxis.Left)
	scales[true].settle(w.YAxis.Right)
	leftCaption := cwWidgetAxisCaption(w.YAxis.Left, lines, false)
	rightCaption := cwWidgetAxisCaption(w.YAxis.Right, lines, true)
	if leftCaption != "" || rightCaption != "" {
		top += 14
	}

	plot := image.Rect(56, top+6, width-12, height-24-legendRows*16)
	if hasRight {
		plot.Max.X = width - 56
	}
	legendX := 0
	if legend == "right" && len(lines) > 0 {
		longest := 0
		for _, l := range lines {
			longest = max(longest, c.textWidth(l.label))
		}
		legendX = width - longest - 28
		plot.Max.X = min(plot.Max.X, legendX-8)
		if hasRight {
			plot.Max.X = legendX - 52
		}
	}

	if w.View == "pie" && plot.Dx() > 4 && plot.Dy() > 4 {
		cwWidgetPie(c, plot, lines)
	} else if plot.Dx() > 4 && plot.Dy() > 4 {
		xOf := func(ts int64) int {
			return plot.Min.X + int(float64(plot.Dx())*float64(ts-start.Unix())/float64(end.Unix()-start.Unix()))
		}
		yOn := func(right bool) func(float64) int {
			sc := scales[right]
			return func(v float64) int { return plot.Max.Y - int(float64(plot.Dy())*(v-sc.lo)/(sc.hi-sc.lo)) }
		}
		yLeft, yRight := yOn(false), yOn(true)
		for i := 0; i <= 4; i++ {
			v := scales[false].lo + (scales[false].hi-scales[false].lo)*float64(i)/4
			y := yLeft(v)
			c.fill(image.Rect(plot.Min.X, y, plot.Max.X, y+1), grid)
			label := strconv.FormatFloat(v, 'g', 4, 64)
			c.text(plot.Min.X-6-c.textWidth(label), y+4, label, fg)
			if hasRight {
				rv := scales[true].lo + (scales[true].hi-scales[true].lo)*float64(i)/4
				c.text(plot.Max.X+6, y+4, strconv.FormatFloat(rv, 'g', 4, 64), fg)
			}
		}
		if leftCaption != "" {
			c.text(8, plot.Min.Y-6, leftCaption, fg)
		}
		if rightCaption != "" {
			c.text(width-8-c.textWidth(rightCaption), plot.Min.Y-6, rightCaption, fg)
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
		if hasRight {
			c.fill(image.Rect(plot.Max.X-1, plot.Min.Y, plot.Max.X, plot.Max.Y), fg)
		}
		clampX := func(x int) int { return max(plot.Min.X, min(plot.Max.X, x)) }
		for _, a := range vertical {
			if !a.visible {
				continue
			}
			x := xOf(a.at.Unix())
			shade := cwWidgetTranslucent(a.color, 0x40)
			switch {
			case a.to != nil:
				x2 := xOf(a.to.Unix())
				c.fill(image.Rect(clampX(min(x, x2)), plot.Min.Y, clampX(max(x, x2)), plot.Max.Y), shade)
			case a.fill == "before":
				c.fill(image.Rect(plot.Min.X, plot.Min.Y, clampX(x), plot.Max.Y), shade)
			case a.fill == "after":
				c.fill(image.Rect(clampX(x), plot.Min.Y, plot.Max.X, plot.Max.Y), shade)
			}
			if x >= plot.Min.X && x <= plot.Max.X {
				c.line(x, plot.Min.Y, x, plot.Max.Y-1, 1, a.color)
				if a.label != "" {
					c.text(x+3, plot.Min.Y+12, a.label, a.color)
				}
			}
		}
		for _, a := range horizontal {
			if !a.visible {
				continue
			}
			yOf := yOn(a.right)
			y := yOf(a.value)
			shade := cwWidgetTranslucent(a.color, 0x40)
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
		case "bar":
			cwWidgetLatestBars(c, plot, lines, yLeft, yRight)
		default:
			for i, l := range lines {
				yOf := yOn(l.right)
				times := l.series.times(false)
				if l.band != nil {
					shade := cwWidgetTranslucent(l.color, 0x50)
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
				if spans, ok := stacked[i]; ok {
					shade := cwWidgetTranslucent(l.color, 0x60)
					step := max(2, plot.Dx()/max(len(times), 1))
					for k, t := range times {
						x := xOf(t)
						next := x + step
						if k+1 < len(times) {
							next = xOf(times[k+1])
						}
						c.fill(image.Rect(x, yOf(spans[t][1]), max(next, x+1), yOf(spans[t][0])), shade)
					}
					for k, t := range times {
						if k > 0 {
							prev := times[k-1]
							c.line(xOf(prev), yOf(spans[prev][1]), xOf(t), yOf(spans[t][1]), 2, l.color)
						}
					}
					continue
				}
				for k, t := range times {
					x, y := xOf(t), yOf(l.series.Points[t])
					if k == 0 {
						c.fill(image.Rect(x-1, y-1, x+2, y+2), l.color)
						continue
					}
					prev := times[k-1]
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

func cwWidgetLatest(l cwWidgetLine) (float64, bool) {
	times := l.series.times(true)
	if len(times) == 0 {
		return 0, false
	}
	return l.series.Points[times[0]], true
}

// The bar view draws each series' latest value as one bar on its y-axis.
func cwWidgetLatestBars(c cwWidgetCanvas, plot image.Rectangle, lines []cwWidgetLine, yLeft, yRight func(float64) int) {
	if len(lines) == 0 {
		return
	}
	slot := plot.Dx() / len(lines)
	for i, l := range lines {
		v, ok := cwWidgetLatest(l)
		if !ok {
			continue
		}
		yOf := yLeft
		if l.right {
			yOf = yRight
		}
		x := plot.Min.X + i*slot + slot/4
		y, zero := yOf(v), min(plot.Max.Y, max(plot.Min.Y, yOf(0)))
		c.fill(image.Rect(x, min(y, zero), x+slot/2, max(y, zero)), l.color)
	}
}

// The pie view draws each series' latest value as a slice of a disc, its
// angle the value's share of the positive latest values.
func cwWidgetPie(c cwWidgetCanvas, plot image.Rectangle, lines []cwWidgetLine) {
	total := 0.0
	values := make([]float64, len(lines))
	for i, l := range lines {
		if v, ok := cwWidgetLatest(l); ok && v > 0 {
			values[i] = v
			total += v
		}
	}
	if total == 0 {
		return
	}
	cx, cy := (plot.Min.X+plot.Max.X)/2, (plot.Min.Y+plot.Max.Y)/2
	radius := float64(min(plot.Dx(), plot.Dy()))/2 - 4
	for y := plot.Min.Y; y < plot.Max.Y; y++ {
		for x := plot.Min.X; x < plot.Max.X; x++ {
			dx, dy := float64(x-cx), float64(y-cy)
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			angle := math.Atan2(dx, -dy)
			if angle < 0 {
				angle += 2 * math.Pi
			}
			share := angle / (2 * math.Pi) * total
			for i, v := range values {
				if share < v {
					c.img.SetRGBA(x, y, lines[i].color)
					break
				}
				share -= v
			}
		}
	}
}

type cwWidgetHorizontal struct {
	value   float64
	to      *float64
	label   string
	color   color.RGBA
	fill    string
	visible bool
	right   bool
}

// cwWidgetAnnotationItems reads an annotations array, each item one
// annotation object or a two-object band.
func cwWidgetAnnotationItems(kind string, raw []json.RawMessage) ([][]cwWidgetAnnotation, error) {
	out := make([][]cwWidgetAnnotation, 0, len(raw))
	for i, item := range raw {
		var band []cwWidgetAnnotation
		if json.Unmarshal(item, &band) == nil {
			if len(band) != 2 {
				return nil, cwWidgetErrorf("The %s band annotation %d must hold two annotation objects.", kind, i)
			}
			out = append(out, band)
			continue
		}
		var single cwWidgetAnnotation
		if err := json.Unmarshal(item, &single); err != nil {
			return nil, cwWidgetErrorf("The %s annotation %d is neither an annotation object nor a band of two.", kind, i)
		}
		out = append(out, []cwWidgetAnnotation{single})
	}
	return out, nil
}

func cwWidgetAnnotationColor(hex string, i int) color.RGBA {
	if col, ok := cwWidgetColor(hex); ok {
		return col
	}
	col, _ := cwWidgetColor(cwWidgetPalette[(i+3)%len(cwWidgetPalette)])
	return col
}

func cwWidgetHorizontalAnnotations(raw []json.RawMessage) ([]cwWidgetHorizontal, error) {
	items, err := cwWidgetAnnotationItems("horizontal", raw)
	if err != nil {
		return nil, err
	}
	var out []cwWidgetHorizontal
	for i, item := range items {
		values := make([]float64, len(item))
		for k, a := range item {
			if err := json.Unmarshal(a.Value, &values[k]); err != nil {
				return nil, cwWidgetErrorf("The value of horizontal annotation %d must be a number.", i)
			}
		}
		first := item[0]
		h := cwWidgetHorizontal{value: values[0], label: first.Label, fill: first.Fill, color: cwWidgetAnnotationColor(first.Color, i),
			visible: first.Visible == nil || *first.Visible, right: first.YAxis == "right"}
		if len(item) == 2 {
			h.to = &values[1]
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].value < out[j].value })
	return out, nil
}

type cwWidgetVertical struct {
	at      time.Time
	to      *time.Time
	label   string
	color   color.RGBA
	fill    string
	visible bool
}

// A vertical annotation's value is an absolute time stamp such as
// 2018-08-28T15:25:26Z.
func cwWidgetVerticalAnnotations(raw []json.RawMessage) ([]cwWidgetVertical, error) {
	items, err := cwWidgetAnnotationItems("vertical", raw)
	if err != nil {
		return nil, err
	}
	var out []cwWidgetVertical
	for i, item := range items {
		times := make([]time.Time, len(item))
		for k, a := range item {
			var s string
			if json.Unmarshal(a.Value, &s) != nil {
				return nil, cwWidgetErrorf("The value of vertical annotation %d must be an absolute time stamp.", i)
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil, cwWidgetErrorf("The value of vertical annotation %d must be an absolute time stamp.", i)
			}
			times[k] = t
		}
		first := item[0]
		v := cwWidgetVertical{at: times[0], label: first.Label, fill: first.Fill, color: cwWidgetAnnotationColor(first.Color, i), visible: first.Visible == nil || *first.Visible}
		if len(item) == 2 {
			v.to = &times[1]
		}
		out = append(out, v)
	}
	return out, nil
}

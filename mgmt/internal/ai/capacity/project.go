// Package capacity is the capacity_forecast agent (M11 S-11): code samples each resource once a day and
// projects growth and exhaustion by least squares; the model only adds confidence and advice.
package capacity

import (
	"math"
	"time"
)

// Resources are the forecast resources in display order (fixed by the M11 spec).
var Resources = []string{"filter_index", "cache", "recursor_cache", "engine_memory", "blocklist_entries", "query_volume"}

// Point is one daily sample.
type Point struct {
	Day   time.Time
	Value float64
}

// Projection is the code-computed forecast of one resource.
type Projection struct {
	Resource                    string
	Current                     float64
	Max                         *float64
	GrowthPerDay, GrowthPerWeek float64
	ExhaustionDate              *time.Time
	DaysRemaining               *int
	Trend                       string // stable|growing|shrinking|insufficient_data
	Points                      int
	MaxConfidence               float64 // 0.5 below 7 points, else 1
}

const (
	minPoints = 3
	// fullConfidencePoints is the number of points below which the model's confidence is capped at 0.5.
	fullConfidencePoints = 7
	// stableShare: growth below this share of the current value per day is stable.
	stableShare = 0.001
	day         = 24 * time.Hour
)

// Project fits a least-squares line over (days since the first point, value) of points in day order.
// Current is the newest value. Fewer than 3 points give insufficient_data with no growth.
func Project(resource string, points []Point, max *float64, now time.Time) Projection {
	p := Projection{Resource: resource, Max: max, Points: len(points), MaxConfidence: 1, Trend: "insufficient_data"}
	if len(points) < fullConfidencePoints {
		p.MaxConfidence = 0.5
	}
	if len(points) == 0 {
		return p
	}
	p.Current = points[len(points)-1].Value
	if len(points) < minPoints {
		return p
	}
	first := points[0].Day
	var sx, sy, sxx, sxy float64
	for _, pt := range points {
		x := pt.Day.Sub(first).Hours() / 24
		sx += x
		sy += pt.Value
		sxx += x * x
		sxy += x * pt.Value
	}
	n := float64(len(points))
	if denom := n*sxx - sx*sx; denom != 0 {
		p.GrowthPerDay = (n*sxy - sx*sy) / denom
	}
	p.GrowthPerWeek = 7 * p.GrowthPerDay
	switch {
	case math.Abs(p.GrowthPerDay) < stableShare*math.Abs(p.Current):
		p.Trend = "stable"
	case p.GrowthPerDay > 0:
		p.Trend = "growing"
	default:
		p.Trend = "shrinking"
	}
	if max != nil && p.GrowthPerDay > 0 && p.Current < *max {
		days := (*max - p.Current) / p.GrowthPerDay
		at := now.Add(time.Duration(days * float64(day)))
		remaining := int(math.Floor(days))
		p.ExhaustionDate, p.DaysRemaining = &at, &remaining
	}
	return p
}

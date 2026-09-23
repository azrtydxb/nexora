package capacity

import controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"

type sampleRow struct {
	value float64
	limit *float64
}

type sampleSet map[string]sampleRow

// Keep each resource's maximum paired with the limit of the same engine.
func (samples sampleSet) higher(resource string, value float64, limit *float64) {
	if s, ok := samples[resource]; !ok || value > s.value {
		samples[resource] = sampleRow{value, limit}
	}
}

func (samples sampleSet) addEngine(s *controlv1.Stats, cacheMax float64) {
	samples.higher("cache", float64(s.CacheBytes), &cacheMax)
	// Missing on older engines, and disabled caches are not zero-valued measurements.
	// Keep the limit paired with the engine supplying the fleet's maximum bytes.
	if rc := s.RecursorCache; rc != nil && rc.Enabled && rc.MaxBytes > 0 {
		limit := float64(rc.MaxBytes)
		samples.higher("recursor_cache", float64(rc.Bytes), &limit)
	}
	if fi := s.FilterIndex; fi != nil {
		limit := float64(fi.MaxBytes)
		samples.higher("filter_index", float64(fi.Bytes), &limit)
	}
	if s.ProcessResidentBytes > 0 {
		var limit *float64
		if s.MemoryLimitBytes > 0 {
			l := float64(s.MemoryLimitBytes)
			limit = &l
		}
		samples.higher("engine_memory", float64(s.ProcessResidentBytes), limit)
	}
}

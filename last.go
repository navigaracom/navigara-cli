package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/navigaracom/navigara-cli/internal/otel"
)

func parseLast(value string) (time.Duration, error) {
	if value == "" {
		return 0, fmt.Errorf("--last must be a positive duration")
	}
	if strings.HasSuffix(value, "d") {
		digits := strings.TrimSuffix(value, "d")
		if digits == "" || strings.Trim(digits, "0123456789") != "" {
			return 0, fmt.Errorf("--last must be a positive duration")
		}
		days, err := strconv.ParseUint(digits, 10, 64)
		if err != nil || days == 0 || days > uint64(time.Duration(1<<63-1)/(24*time.Hour)) {
			return 0, fmt.Errorf("--last must be a positive duration")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("--last must be a positive duration")
	}
	return duration, nil
}

func filterPoints(points []otel.Point, since, until time.Time) ([]otel.Point, int) {
	kept := points[:0]
	for _, point := range points {
		if (!since.IsZero() && point.Time.Before(since)) || (!until.IsZero() && point.Time.After(until)) {
			continue
		}
		kept = append(kept, point)
	}
	return kept, len(points) - len(kept)
}

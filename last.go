package main

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var errLast = errors.New("--last must be a positive duration")

func parseLast(value string) (time.Duration, error) {
	if digits, ok := strings.CutSuffix(value, "d"); ok {
		days, err := strconv.ParseUint(digits, 10, 64)
		if err != nil || days == 0 || days > uint64(time.Duration(1<<63-1)/(24*time.Hour)) {
			return 0, errLast
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, errLast
	}
	return duration, nil
}

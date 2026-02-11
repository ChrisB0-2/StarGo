package tle

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Parse reads 3-line NORAD TLE format from r and returns parsed entries.
// Malformed entries are skipped with a warning log.
func Parse(r io.Reader, logger *slog.Logger) ([]TLEEntry, error) {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading TLE data: %w", err)
	}

	var entries []TLEEntry
	for i := 0; i+2 < len(lines); {
		name := lines[i]
		line1 := lines[i+1]
		line2 := lines[i+2]

		// Validate line prefixes.
		if !strings.HasPrefix(line1, "1 ") || !strings.HasPrefix(line2, "2 ") {
			// Try to find next valid triplet.
			logger.Warn("skipping malformed TLE entry", "line_index", i, "name", name)
			i++
			continue
		}

		// Extract NORAD ID from line1 cols 3-7 (0-indexed: 2..7).
		noradStr := strings.TrimSpace(line1[2:7])
		noradID, err := strconv.Atoi(noradStr)
		if err != nil {
			logger.Warn("skipping TLE entry with invalid NORAD ID", "norad_str", noradStr, "name", name)
			i += 3
			continue
		}

		// Extract epoch from line1 cols 19-32 (0-indexed: 18..32).
		if len(line1) < 32 {
			logger.Warn("skipping TLE entry with short line1", "name", name)
			i += 3
			continue
		}
		epochStr := strings.TrimSpace(line1[18:32])
		epoch, err := parseEpoch(epochStr)
		if err != nil {
			logger.Warn("skipping TLE entry with invalid epoch", "epoch_str", epochStr, "name", name, "error", err)
			i += 3
			continue
		}

		entries = append(entries, TLEEntry{
			NORADID: noradID,
			Name:    strings.TrimSpace(name),
			Epoch:   epoch,
			Line1:   line1,
			Line2:   line2,
		})
		i += 3
	}

	return entries, nil
}

// parseEpoch converts a TLE epoch string in YYDDD.DDDDDDDD format to time.Time.
// Year 00-56 → 2000s, 57-99 → 1900s.
func parseEpoch(s string) (time.Time, error) {
	if len(s) < 5 {
		return time.Time{}, fmt.Errorf("epoch string too short: %q", s)
	}

	yearStr := s[:2]
	dayStr := s[2:]

	year, err := strconv.Atoi(yearStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid epoch year %q: %w", yearStr, err)
	}

	if year >= 57 {
		year += 1900
	} else {
		year += 2000
	}

	dayOfYear, err := strconv.ParseFloat(dayStr, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid epoch day %q: %w", dayStr, err)
	}

	// Start of the year, then add fractional days.
	t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	// dayOfYear is 1-based: day 1 = Jan 1.
	dur := time.Duration((dayOfYear - 1) * float64(24*time.Hour))
	t = t.Add(dur)

	return t, nil
}

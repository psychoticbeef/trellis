package testreport

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FileCov is one file's line/statement coverage.
type FileCov struct {
	Path    string
	Covered int
	Total   int
}

// ParseCoverage reads all files matching glob (relative to dir) and returns
// per-file coverage, detecting LCOV and Go coverprofile per file. Coverage is
// observability, never a gate — callers turn errors into notices.
func ParseCoverage(dir, glob string) ([]FileCov, error) {
	pattern := glob
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(dir, glob)
	}
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad coverage glob %q: %w", glob, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no coverage files match %q", glob)
	}
	agg := map[string]*FileCov{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return nil, fmt.Errorf("coverage file %s is empty", f)
		}
		var parseErr error
		if strings.HasPrefix(text, "mode:") {
			parseErr = parseGoCover(text, agg)
		} else if strings.Contains(text, "\nSF:") || strings.HasPrefix(text, "SF:") || strings.HasPrefix(text, "TN:") {
			parseErr = parseLCOV(text, agg)
		} else {
			parseErr = fmt.Errorf("unrecognized coverage format in %s", f)
		}
		if parseErr != nil {
			return nil, parseErr
		}
	}
	out := make([]FileCov, 0, len(agg))
	for _, fc := range agg {
		out = append(out, *fc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// parseLCOV aggregates DA: line hits per SF: section (LF/LH are ignored in
// favor of counting DA lines, which works across generators).
func parseLCOV(text string, agg map[string]*FileCov) error {
	var cur *FileCov
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	seen := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "SF:"):
			path := strings.TrimPrefix(line, "SF:")
			if agg[path] == nil {
				agg[path] = &FileCov{Path: path}
			}
			cur = agg[path]
			seen = true
		case strings.HasPrefix(line, "DA:") && cur != nil:
			parts := strings.SplitN(strings.TrimPrefix(line, "DA:"), ",", 3)
			if len(parts) < 2 {
				return fmt.Errorf("malformed lcov DA line %q", line)
			}
			hits, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				return fmt.Errorf("malformed lcov DA line %q", line)
			}
			cur.Total++
			if hits > 0 {
				cur.Covered++
			}
		case line == "end_of_record":
			cur = nil
		}
	}
	if !seen {
		return fmt.Errorf("lcov input without SF records")
	}
	return nil
}

// parseGoCover aggregates statement counts from a Go coverprofile.
func parseGoCover(text string, agg map[string]*FileCov) error {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			first = false
			continue // mode: line
		}
		if line == "" {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			return fmt.Errorf("malformed coverprofile line %q", line)
		}
		path := line[:colon]
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			return fmt.Errorf("malformed coverprofile line %q", line)
		}
		stmts, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("malformed coverprofile line %q", line)
		}
		if agg[path] == nil {
			agg[path] = &FileCov{Path: path}
		}
		agg[path].Total += stmts
		if count > 0 {
			agg[path].Covered += stmts
		}
	}
	return nil
}

// Pct is a file's coverage percentage (100 for empty files).
func (f FileCov) Pct() float64 {
	if f.Total == 0 {
		return 100
	}
	return float64(f.Covered) * 100 / float64(f.Total)
}

// Summarize computes the overall percentage and the worst-covered files
// (ascending by percentage, path as tiebreaker), capped at max.
func Summarize(files []FileCov, max int) (totalPct float64, worst []FileCov) {
	var covered, total int
	for _, f := range files {
		covered += f.Covered
		total += f.Total
	}
	if total > 0 {
		totalPct = float64(covered) * 100 / float64(total)
	} else {
		totalPct = 100
	}
	worst = append([]FileCov(nil), files...)
	sort.Slice(worst, func(i, j int) bool {
		if worst[i].Pct() != worst[j].Pct() {
			return worst[i].Pct() < worst[j].Pct()
		}
		return worst[i].Path < worst[j].Path
	})
	if len(worst) > max {
		worst = worst[:max]
	}
	return totalPct, worst
}

package coordination

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	issueRefPattern = regexp.MustCompile(`(?i)(?:^|[\s(])(?:(?:fixes|closes|resolves|refs|ref|see|part of|relates to)\s+)?#([1-9][0-9]*)\b`)
	lockfileNames   = map[string]bool{
		"go.sum":            true,
		"package-lock.json": true,
		"yarn.lock":         true,
		"pnpm-lock.yaml":    true,
		"cargo.lock":        true,
		"poetry.lock":       true,
		"gemfile.lock":      true,
		"composer.lock":     true,
		"uv.lock":           true,
		"flake.lock":        true,
	}
)

// ExtractLinkedIssues parses explicit GitHub issue references (such as "#231", "Fixes #309")
// from PR titles and bodies, returning deduplicated sorted issue numbers.
func ExtractLinkedIssues(texts ...string) []int {
	seen := make(map[int]bool)
	for _, txt := range texts {
		for _, m := range issueRefPattern.FindAllStringSubmatch(txt, -1) {
			if len(m) < 2 {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err == nil && n > 0 {
				seen[n] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// IsIncidentalPath reports whether p is a lockfile or generated manifest whose
// shared modification should not trigger overlap or scope-collision alerts (docs/PROJECT.md §5).
func IsIncidentalPath(p string) bool {
	base := strings.ToLower(path.Base(cleanPath(p)))
	return lockfileNames[base]
}

// FilterNonIncidentalPaths removes lockfiles and normalizes paths.
func FilterNonIncidentalPaths(paths []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, raw := range paths {
		p := cleanPath(raw)
		if p == "" || IsIncidentalPath(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func cleanPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return ""
	}
	return path.Clean(p)
}

// PathMatches reports whether a target file path matches a declared scope pattern
// (exact file, directory prefix, or glob with "*" / "**").
func PathMatches(pattern, target string) bool {
	pat := cleanPath(pattern)
	tgt := cleanPath(target)
	if pat == "" || tgt == "" || IsIncidentalPath(tgt) {
		return false
	}
	if pat == tgt {
		return true
	}
	// Directory prefix match (e.g. "internal/lexer" or "internal/lexer/**").
	if strings.HasSuffix(pat, "/**") {
		prefix := strings.TrimSuffix(pat, "/**")
		return tgt == prefix || strings.HasPrefix(tgt, prefix+"/")
	}
	if strings.HasSuffix(pat, "/*") {
		prefix := strings.TrimSuffix(pat, "/*")
		return strings.HasPrefix(tgt, prefix+"/") && !strings.Contains(strings.TrimPrefix(tgt, prefix+"/"), "/")
	}
	if strings.HasPrefix(tgt, pat+"/") {
		return true
	}
	ok, err := path.Match(pat, tgt)
	return err == nil && ok
}

// PathsOverlap reports whether any non-incidental pattern in a matches any non-incidental target in b (symmetrically).
func PathsOverlap(a, b []string) (bool, string) {
	fa := FilterNonIncidentalPaths(a)
	fb := FilterNonIncidentalPaths(b)
	for _, pa := range fa {
		for _, pb := range fb {
			if PathMatches(pa, pb) || PathMatches(pb, pa) {
				return true, pb
			}
		}
	}
	return false, ""
}

// SharedInts returns sorted integers present in both a and b.
func SharedInts(a, b []int) []int {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[int]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	var out []int
	seen := make(map[int]bool)
	for _, v := range b {
		if set[v] && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// SharedStrings returns sorted case-insensitive matches present in both a and b.
func SharedStrings(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]string, len(a))
	for _, v := range a {
		k := strings.ToLower(strings.TrimSpace(v))
		if k != "" {
			set[k] = strings.TrimSpace(v)
		}
	}
	var out []string
	seen := make(map[string]bool)
	for _, v := range b {
		k := strings.ToLower(strings.TrimSpace(v))
		if orig, ok := set[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, orig)
		}
	}
	slices.Sort(out)
	return out
}

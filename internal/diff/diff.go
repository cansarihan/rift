// Package diff compares two builds of the same program function by function and
// grades each change by how much it looks like a quietly shipped security fix.
// The strongest signal is a function that begins calling the runtime's bounds,
// slice and overflow check helpers where it did not before — the machine-code
// trace of an added safety check.
package diff

import (
	"context"
	"sort"
	"strings"

	"github.com/cansarihan/rift/internal/binfile"
	"github.com/cansarihan/rift/internal/callscan"
)

// Status is how a function changed between the two builds.
type Status string

const (
	StatusModified Status = "modified"
	StatusAdded    Status = "added"
	StatusRemoved  Status = "removed"
)

// Band is the suspicion level that a change hides a security fix.
const (
	BandLikely   = "likely"
	BandPossible = "possible"
	BandUnlikely = "unlikely"
	BandClean    = "clean"
)

// FuncDelta is one function that differs between the two builds, with the
// evidence Rift used to grade it.
type FuncDelta struct {
	Name          string   `json:"name"`
	Status        Status   `json:"status"`
	OldSize       uint64   `json:"old_size"`
	NewSize       uint64   `json:"new_size"`
	Churn         float64  `json:"churn"`
	AddedCalls    []string `json:"added_calls,omitempty"`
	RemovedCalls  []string `json:"removed_calls,omitempty"`
	AddedGuards   []string `json:"added_guards,omitempty"`
	RemovedGuards []string `json:"removed_guards,omitempty"`
	Score         int      `json:"score"`
	Band          string   `json:"band"`
	Reasons       []string `json:"reasons,omitempty"`
}

// Summary is the whole comparison: a count of what changed and the ranked list
// of changes, worst first.
type Summary struct {
	Old      string         `json:"old"`
	New      string         `json:"new"`
	Arch     string         `json:"arch"`
	OldFuncs int            `json:"old_funcs"`
	NewFuncs int            `json:"new_funcs"`
	Compared int            `json:"compared"`
	Modified int            `json:"modified"`
	Added    int            `json:"added"`
	Removed  int            `json:"removed"`
	Flagged  int            `json:"flagged"`
	Counts   map[string]int `json:"counts"`
	Index    int            `json:"index"`
	Band     string         `json:"band"`
	Deltas   []FuncDelta    `json:"deltas"`
}

// UpdateType tags a streamed update for the panel.
type UpdateType string

const (
	UpdateDelta    UpdateType = "delta"
	UpdateProgress UpdateType = "progress"
)

// Update is a single event emitted while a comparison runs.
type Update struct {
	Type  UpdateType `json:"type"`
	Delta *FuncDelta `json:"delta,omitempty"`
	Done  int        `json:"done,omitempty"`
	Total int        `json:"total,omitempty"`
}

// Run compares old against new and returns the graded summary. If emit is not
// nil it receives every flagged delta and periodic progress, in address order,
// so a panel can draw the two builds filling in live.
func Run(ctx context.Context, base, updated *binfile.Image, emit func(Update)) (Summary, error) {
	arch := string(base.Arch)
	if base.Arch == binfile.ArchUnknown {
		arch = string(updated.Arch)
	}

	summary := Summary{
		Old:      base.Path,
		New:      updated.Path,
		Arch:     arch,
		OldFuncs: len(base.Funcs),
		NewFuncs: len(updated.Funcs),
		Counts:   map[string]int{BandLikely: 0, BandPossible: 0, BandUnlikely: 0},
	}

	total := len(updated.Funcs) + len(base.Funcs)
	done := 0
	progress := func() {
		done++
		if emit != nil && done%64 == 0 {
			emit(Update{Type: UpdateProgress, Done: done, Total: total})
		}
	}

	var deltas []FuncDelta
	record := func(d FuncDelta) {
		deltas = append(deltas, d)
		if emit != nil {
			copyDelta := d
			emit(Update{Type: UpdateDelta, Delta: &copyDelta})
		}
	}

	// Functions present in the new build: modified or added.
	for _, nf := range updated.Funcs {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		of, existed := base.Lookup(nf.Name)
		if !existed {
			summary.Added++
			record(gradeAdded(updated.Arch, nf, updated))
			progress()
			continue
		}
		summary.Compared++
		if equalNormalized(base.Arch, of, nf) {
			progress()
			continue
		}
		delta := gradeModified(base, updated, of, nf)
		if isLayoutNoise(delta) || len(delta.Reasons) == 0 {
			progress()
			continue
		}
		summary.Modified++
		record(delta)
		progress()
	}

	// Functions only in the old build: removed.
	for _, of := range base.Funcs {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if _, stillThere := updated.Lookup(of.Name); stillThere {
			progress()
			continue
		}
		summary.Removed++
		record(gradeRemoved(base.Arch, of, base))
		progress()
	}

	sort.SliceStable(deltas, func(i, j int) bool {
		if deltas[i].Score != deltas[j].Score {
			return deltas[i].Score > deltas[j].Score
		}
		return deltas[i].Name < deltas[j].Name
	})

	for _, d := range deltas {
		summary.Counts[d.Band]++
		if d.Band == BandLikely || d.Band == BandPossible {
			summary.Flagged++
		}
		if d.Score > summary.Index {
			summary.Index = d.Score
		}
	}
	summary.Deltas = deltas
	summary.Band = bandFor(summary.Index)
	if len(deltas) == 0 {
		summary.Band = BandClean
	}

	if emit != nil {
		emit(Update{Type: UpdateProgress, Done: total, Total: total})
	}
	return summary, nil
}

func gradeModified(oldImg, newImg *binfile.Image, of, nf binfile.Func) FuncDelta {
	oldCalls := callscan.Callees(oldImg.Arch, of, oldImg)
	newCalls := callscan.Callees(newImg.Arch, nf, newImg)
	added := subtract(newCalls, oldCalls)
	removed := subtract(oldCalls, newCalls)
	addedGuards := guardsAmong(added)
	removedGuards := guardsAmong(removed)

	d := FuncDelta{
		Name:          nf.Name,
		Status:        StatusModified,
		OldSize:       of.Size,
		NewSize:       nf.Size,
		Churn:         churn(callscan.NormalizedCode(oldImg.Arch, of), callscan.NormalizedCode(newImg.Arch, nf)),
		AddedCalls:    added,
		RemovedCalls:  removed,
		AddedGuards:   addedGuards,
		RemovedGuards: removedGuards,
	}

	score := 0
	// A change to the set of runtime bounds/overflow checks is the strongest
	// signal either way: a check appearing, or one eliminated because the
	// function now validates its inputs up front, is exactly what a quiet fix
	// looks like in machine code.
	if guardChange := len(addedGuards) + len(removedGuards); guardChange > 0 {
		score += 45 + min(guardChange, 4)*8
		if len(removedGuards) > 0 {
			d.Reasons = append(d.Reasons, "drops "+countNoun(len(removedGuards), "runtime bounds/overflow check")+" ("+strings.Join(removedGuards, ", ")+")")
		}
		if len(addedGuards) > 0 {
			d.Reasons = append(d.Reasons, "adds "+countNoun(len(addedGuards), "runtime bounds/overflow check")+" ("+strings.Join(addedGuards, ", ")+")")
		}
	}
	// A new call to an error constructor is the machine-code trace of a rejection
	// path that was not there before — the shape of an added input check.
	errPaths := errorPaths(added)
	if len(errPaths) > 0 {
		score += 32
		d.Reasons = append(d.Reasons, "adds an error/validation path ("+strings.Join(errPaths, ", ")+")")
	}
	if grew := int(nf.Size) - int(of.Size); grew > 0 {
		score += min(grew/4, 20)
		d.Reasons = append(d.Reasons, "grew by "+plural(grew, "byte"))
	}
	if extra := len(added) - len(addedGuards) - len(errPaths); extra > 0 {
		score += min(extra*3, 12)
		d.Reasons = append(d.Reasons, countNoun(extra, "new call"))
	}
	if d.Churn > 0 && d.Churn <= 0.5 {
		score += 8
		d.Reasons = append(d.Reasons, "change stays localized")
	}
	d.Score = min(score, 100)
	d.Band = bandFor(d.Score)
	return d
}

func gradeAdded(arch binfile.Arch, nf binfile.Func, img *binfile.Image) FuncDelta {
	calls := callscan.Callees(arch, nf, img)
	guards := guardsAmong(calls)
	d := FuncDelta{
		Name:        nf.Name,
		Status:      StatusAdded,
		NewSize:     nf.Size,
		AddedCalls:  calls,
		AddedGuards: guards,
	}
	// A wholly new function is weaker evidence than a changed one — a build that
	// links in more library code grows many functions that were simply not
	// referenced before. It is scored low unless it also carries the shape of a
	// validation helper: bounds checks together with an error path of its own.
	score := 8
	d.Reasons = append(d.Reasons, "new function")
	if len(guards) > 0 {
		score += 12
	}
	if ep := errorPaths(calls); len(ep) > 0 {
		score += 18
		d.Reasons = append(d.Reasons, "builds an error/validation path ("+strings.Join(ep, ", ")+")")
	}
	d.Score = min(score, 100)
	d.Band = bandFor(d.Score)
	return d
}

func gradeRemoved(arch binfile.Arch, of binfile.Func, img *binfile.Image) FuncDelta {
	calls := callscan.Callees(arch, of, img)
	d := FuncDelta{
		Name:         of.Name,
		Status:       StatusRemoved,
		OldSize:      of.Size,
		RemovedCalls: calls,
		Score:        6,
		Reasons:      []string{"function removed"},
	}
	d.Band = bandFor(d.Score)
	return d
}

func bandFor(score int) string {
	switch {
	case score >= 60:
		return BandLikely
	case score >= 30:
		return BandPossible
	default:
		return BandUnlikely
	}
}

// guardNames recognises the runtime helpers Go calls when a bounds, slice,
// shift or conversion check fails. A call to one of these appearing in a build
// that lacked it is the clearest machine-code evidence of an added check.
func isGuard(name string) bool {
	switch {
	case strings.HasPrefix(name, "runtime.panic"),
		strings.HasPrefix(name, "runtime.goPanic"),
		strings.HasPrefix(name, "runtime.checkptr"):
		return true
	case strings.Contains(name, "Bounds"), strings.Contains(name, "bounds"):
		return strings.HasPrefix(name, "runtime.")
	default:
		return false
	}
}

// errorPaths picks out calls that build or return an error, the trace a newly
// added rejection path leaves behind. The boxing helpers the compiler inserts
// around error arguments are deliberately not counted here.
func isErrorPath(name string) bool {
	switch {
	case strings.Contains(name, "errorf"), strings.Contains(name, "Errorf"):
		return true
	case strings.HasPrefix(name, "errors."):
		return true
	case strings.HasSuffix(name, ".Error"):
		return true
	default:
		return false
	}
}

func errorPaths(names []string) []string {
	var out []string
	for _, n := range names {
		if isErrorPath(n) {
			out = append(out, n)
		}
	}
	return out
}

func guardsAmong(names []string) []string {
	var out []string
	for _, n := range names {
		if isGuard(n) {
			out = append(out, n)
		}
	}
	return out
}

func subtract(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := set[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// equalNormalized reports whether two functions are the same once layout-only
// operands are masked. A size difference is treated as a real change outright.
func equalNormalized(arch binfile.Arch, a, b binfile.Func) bool {
	if len(a.Code) != len(b.Code) {
		return false
	}
	return bytesEqual(callscan.NormalizedCode(arch, a), callscan.NormalizedCode(arch, b))
}

// isLayoutNoise reports whether a modified delta carries no behavioural signal:
// the call set is unchanged, the size is unchanged, and the residual churn is
// small. That pattern is almost always relocated data references, not a code
// change, so Rift drops it rather than counting it as a modification.
func isLayoutNoise(d FuncDelta) bool {
	return len(d.AddedCalls) == 0 && len(d.RemovedCalls) == 0 && d.OldSize == d.NewSize && d.Churn < 0.5
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// churn is one minus the Jaccard similarity of the two functions' 8-byte code
// shingles: 0 for identical code, approaching 1 for a full rewrite. Shingling
// tolerates the address shifts a recompile introduces, so a small logic change
// reads as a small churn rather than a total difference.
func churn(a, b []byte) float64 {
	sa := shingles(a)
	sb := shingles(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 0
	}
	inter := 0
	for h := range sa {
		if _, ok := sb[h]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return 1 - float64(inter)/float64(union)
}

func shingles(code []byte) map[uint64]struct{} {
	const window = 8
	set := make(map[uint64]struct{})
	if len(code) < window {
		if len(code) > 0 {
			var h uint64 = 1469598103934665603
			for _, c := range code {
				h = (h ^ uint64(c)) * 1099511628211
			}
			set[h] = struct{}{}
		}
		return set
	}
	for i := 0; i+window <= len(code); i++ {
		var h uint64 = 1469598103934665603
		for _, c := range code[i : i+window] {
			h = (h ^ uint64(c)) * 1099511628211
		}
		set[h] = struct{}{}
	}
	return set
}

func plural(n int, noun string) string {
	s := itoa(n) + " " + noun
	if n != 1 {
		s += "s"
	}
	return s
}

func countNoun(n int, noun string) string { return plural(n, noun) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

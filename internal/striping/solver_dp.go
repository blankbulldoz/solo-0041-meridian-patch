package striping

import (
	"math"
)

// score is the lexicographic partition objective. Deviation is scaled by the
// band count so comparisons against the rational target total/bands stay in
// exact integer arithmetic: N·covered − total is always an integer.
type score struct {
	maxAbs int64 // max |N·covered(b) − total|
	sumSq  int64 // sum (N·covered(b) − total)²
}

func better(a, b score) bool {
	if a.maxAbs != b.maxAbs {
		return a.maxAbs < b.maxAbs
	}
	return a.sumSq < b.sumSq
}

type dpCell struct {
	score score
	prev  int
	valid bool
}

// maxSafeSquare bounds values whose square still fits int64 (≈ 3.03e9).
const maxSafeSquare int64 = 3_037_000_049

// bandScore evaluates one band's contribution with overflow protection.
func bandScore(covered, bands, total int64) (score, bool) {
	product := bands * covered
	if covered != 0 && product/covered != bands {
		return score{}, false
	}
	diff := product - total
	abs := diff
	if abs < 0 {
		abs = -abs
		if abs < 0 {
			return score{}, false
		}
	}
	if abs > maxSafeSquare {
		return score{}, false
	}
	return score{maxAbs: abs, sumSq: diff * diff}, true
}

// exactDynamicCuts solves the partition globally for tractable integer widths.
// State dp[k][i] is the best lexicographic score tiling columns [0,i) with k
// positive-width bands. Parent rows are retained to reconstruct the cuts.
func exactDynamicCuts(width int64, bands int, total int64, prefix []int64) []int64 {
	n := int(width)
	inf := score{maxAbs: math.MaxInt64, sumSq: math.MaxInt64}

	rows := make([][]dpCell, bands+1)
	rows[0] = make([]dpCell, n+1)
	for i := range rows[0] {
		rows[0][i].score = inf
	}
	rows[0][0].valid = true
	rows[0][0].score = score{}

	for k := 1; k <= bands; k++ {
		row := make([]dpCell, n+1)
		for i := range row {
			row[i].score = inf
		}
		// A band needs at least one column: k bands ending at i need i >= k.
		for i := k; i <= n; i++ {
			for j := k - 1; j < i; j++ {
				parent := rows[k-1][j]
				if !parent.valid {
					continue
				}
				s, ok := bandScore(prefix[i]-prefix[j], int64(bands), total)
				if !ok {
					continue
				}
				sumSq := parent.score.sumSq + s.sumSq
				if sumSq < 0 {
					continue
				}
				candidate := score{
					maxAbs: max(parent.score.maxAbs, s.maxAbs),
					sumSq:  sumSq,
				}
				if !row[i].valid || better(candidate, row[i].score) {
					row[i] = dpCell{score: candidate, prev: j, valid: true}
				}
			}
		}
		rows[k] = row
	}

	cuts := make([]int64, 0, bands-1)
	i := n
	for k := bands; k >= 2; k-- {
		j := rows[k][i].prev
		cuts = append(cuts, int64(j))
		i = j
	}
	slicesReverseInt64(cuts)
	return cuts
}

func slicesReverseInt64(s []int64) {
	for left, right := 0, len(s)-1; left < right; left, right = left+1, right-1 {
		s[left], s[right] = s[right], s[left]
	}
}

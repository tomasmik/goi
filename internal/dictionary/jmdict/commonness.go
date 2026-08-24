package jmdict

// CommonnessScore converts JMdict priority markers into the 1–100 estimate
// shown by Goi. It is not a corpus frequency rank.
func CommonnessScore(priority int) int {
	if priority < 0 {
		return 1
	}
	const rankBase = unrankedPriority + 1
	readingRank := priority / rankBase
	writtenRank := priority % rankBase
	rank := min(readingRank, writtenRank)
	switch {
	case rank == 0:
		return 75
	case rank == 10:
		return 35
	case rank >= 21 && rank <= 68:
		return 100 - (rank-21)*94/47
	default:
		return 1
	}
}

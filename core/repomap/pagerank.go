package repomap

import "sort"

const (
	pageRankIterations = 30
	pageRankDamping    = 0.85
)

// ScoredSymbol carries a symbol plus its PageRank score and inbound edge count.
type ScoredSymbol struct {
	Symbol   Symbol
	Score    float64
	InDegree int
}

// Rank computes PageRank-style scores for every symbol across all files.
//
// Graph construction:
//   - Each (file, symbol-name) pair becomes a node.
//   - For each Reference, edges go from EVERY def in the referencing file
//     to EVERY def whose name matches the reference.name (across all files).
//   - Multi-edges allowed (same source-target pair multiple times).
//
// Algorithm: damping=0.85, 30 iterations, uniform initial distribution.
//
// Returns ScoredSymbol entries sorted by Score descending. Within equal
// scores, sorted by Symbol.File then Symbol.Name (deterministic).
func Rank(symbols []*FileSymbols) []ScoredSymbol {
	if len(symbols) == 0 {
		return nil
	}

	// Build the node list: stable order = (file, defIndex)
	type nodeID = int
	var nodes []Symbol
	fileToDefs := map[string][]int{}  // file → indices of nodes from that file
	nameToNodes := map[string][]int{} // name → indices of nodes with that name
	for _, fs := range symbols {
		for _, d := range fs.Defs {
			id := len(nodes)
			nodes = append(nodes, d)
			fileToDefs[fs.File] = append(fileToDefs[fs.File], id)
			nameToNodes[d.Name] = append(nameToNodes[d.Name], id)
		}
	}
	n := len(nodes)
	if n == 0 {
		return nil
	}

	// Build adjacency: outEdges[from] = list of "to" node IDs (multi-edges allowed)
	outEdges := make([][]int, n)
	inDegree := make([]int, n)

	for _, fs := range symbols {
		srcIDs := fileToDefs[fs.File]
		if len(srcIDs) == 0 {
			continue
		}
		for _, ref := range fs.Refs {
			targets := nameToNodes[ref.Name]
			if len(targets) == 0 {
				continue
			}
			for _, src := range srcIDs {
				for _, tgt := range targets {
					if src == tgt {
						continue
					}
					outEdges[src] = append(outEdges[src], tgt)
					inDegree[tgt]++
				}
			}
		}
	}

	// Iterate PageRank
	score := make([]float64, n)
	next := make([]float64, n)
	init := 1.0 / float64(n)
	for i := range score {
		score[i] = init
	}
	for iter := 0; iter < pageRankIterations; iter++ {
		// Reset next with the (1-d)/N base
		base := (1.0 - pageRankDamping) / float64(n)
		for i := range next {
			next[i] = base
		}
		// Distribute current scores along outEdges
		for src, targets := range outEdges {
			if len(targets) == 0 {
				// Dangling node: distribute its score uniformly to all (smooths convergence)
				contrib := pageRankDamping * score[src] / float64(n)
				for j := range next {
					next[j] += contrib
				}
				continue
			}
			contrib := pageRankDamping * score[src] / float64(len(targets))
			for _, tgt := range targets {
				next[tgt] += contrib
			}
		}
		score, next = next, score
	}

	// Build output
	out := make([]ScoredSymbol, n)
	for i := range nodes {
		out[i] = ScoredSymbol{
			Symbol:   nodes[i],
			Score:    score[i],
			InDegree: inDegree[i],
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Symbol.File != out[j].Symbol.File {
			return out[i].Symbol.File < out[j].Symbol.File
		}
		return out[i].Symbol.Name < out[j].Symbol.Name
	})
	return out
}

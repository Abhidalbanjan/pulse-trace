// Standalone module: the bench harness is a tool, not part of the product, and
// keeping it out of the service modules stops it pulling deps into anything
// that ships. Stdlib only, deliberately — a benchmark's input generator should
// have nothing to go wrong in it.
module github.com/pulsetrace/bench/corpus

go 1.26.0

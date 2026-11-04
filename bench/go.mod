module github.com/x-mesh/fastcompress/bench

go 1.25

require (
	github.com/klauspost/compress v1.20.3
	github.com/x-mesh/fastcompress v0.0.0
)

// the benchmark always measures the fastcompress in this repository
replace github.com/x-mesh/fastcompress => ../

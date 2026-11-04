// Package zstd decodes and encodes zstd (RFC 8878) data in Go, without cgo.
//
// Decode and Reader decode zstd frames, and Encode and Writer encode them at
// level 1 (zstd's fast strategy) or 3 (double fast). On amd64 with BMI2,
// assembly decodes and encodes the sequences, decodes the Huffman literals and
// executes the sequences. Large inputs use a second goroutine with more than
// one CPU.
package zstd

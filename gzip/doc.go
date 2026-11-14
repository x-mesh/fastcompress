// Package gzip decodes and encodes gzip (RFC 1952) and DEFLATE (RFC 1951)
// data in Go, without cgo.
//
// Reader replaces compress/gzip.Reader for reading: NewReader, Read, Reset and
// Close behave the same, and concatenated members are read as one stream.
// Reader does not expose the gzip header fields (Name, Comment, ModTime).
// A Read decodes about as much as it asks for, so a scan that reads the start
// of many streams does not decode each one to the end. InflateReader reads a
// raw DEFLATE stream, such as a zip entry, in the same way.
// Gunzip and Inflate decode data that is already in memory; InflateLimit
// stops at a limit on the output size, for data from an unknown source.
//
// Writer replaces compress/gzip.Writer for writing, with the same levels and
// the methods NewWriter, NewWriterLevel, Write, Flush, Close and Reset.
// Writer does not write the header fields. Gzip and Deflate encode data that
// is already in memory; without Flush, a Writer writes the same bytes as Gzip.
// GzipConcurrent and Writer.SetConcurrency compress chunks of about 1 MB on
// several goroutines; the output does not depend on their number.
//
// The decoder uses a 64-bit bit buffer refilled with one 8-byte load, table
// entries that count their extra bits so one shift consumes a code and its
// extra bits, table loads overlapped with refills, decoding the next symbol
// before a match copy, and wide match copies. On amd64 with BMI2, assembly
// runs the main decode loop; the purego build tag selects the Go loop. On
// arm64 the CRC-32 folds eight 128-bit lanes with PMULL in assembly;
// elsewhere it uses hash/crc32.
//
// The encoder finds matches with one hash table at levels 1-3, with a 4-byte
// and an 8-byte hash table at levels 4-6, and with zlib-style hash chains and
// lazy matching at levels 7-9. It writes each block as the smallest of a
// stored, fixed or dynamic block.
package gzip

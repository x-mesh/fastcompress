//go:build !arm64 || purego

package zstd

const useFusedAsm = false

// On a Xeon Silver 4112, a third goroutine for the literals made Decode
// 16-24% faster on binaries but 13% slower on a log at level 3, where the
// execution already took as long as the parsing.
const splitLits = false

// runFused decodes the sequences of a compressed block, then executes them
// with x.
func (d *zdec) runFused(x *zexec, out []byte, it *zitem) ([]byte, error) {
	d.parseSeqs(it)
	if it.err != nil {
		return out, it.err
	}
	return x.run(out, it)
}

//go:build !arm64 || purego

package zstd

const useFusedAsm = false

// runFused decodes the sequences of a compressed block, then executes them
// with x.
func (d *zdec) runFused(x *zexec, out []byte, it *zitem) ([]byte, error) {
	d.parseSeqs(it)
	if it.err != nil {
		return out, it.err
	}
	return x.run(out, it)
}

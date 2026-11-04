//go:build !arm64 || purego

package zstd

const useFusedAsm = false

// runFused decodes the sequences of a compressed block, then executes them.
func (d *zdec) runFused(out []byte, it *zitem) ([]byte, error) {
	d.parseSeqs(it)
	if it.err != nil {
		return out, it.err
	}
	return d.x.run(out, it)
}

//go:build !arm64 || purego

package zstd

const useFusedAsm = false

// On a Xeon Silver 4112 with 4 cores and 8 threads, a third goroutine for
// the literals made Decode 16-24% faster on binaries but 13% slower on a log
// at level 3. With one thread on each core, it made every file faster: the
// loss came from busy goroutines that shared a core through hyper-threading,
// and a library cannot choose the cores of its goroutines.
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

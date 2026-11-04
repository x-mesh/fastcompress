//go:build !amd64 || purego

package gzip

const useFastAsm = false

func (d *decoder) fastAsm([]uint32, []uint32, uint64, uint64) (bool, error) { return false, nil }

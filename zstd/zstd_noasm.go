//go:build !amd64 || purego

package zstd

const useHufAsm = false

func (h *hufTable) decode4Asm(*[4]revReader, [5]int, []byte, []byte, int) int { return 0 }

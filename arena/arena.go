//go:build linux && (amd64 || arm64 || riscv64)

// Package arena selects non-moving reclamation for the shared utils allocator.
// Payloads must not contain Go heap pointers. Close requires all users to finish.
package arena

import "github.com/banditmoscow1337/utils"

type Arena = utils.Arena
type Stats = utils.Stats
type BlockHeader = utils.BlockHeader

const (
	RegionSize     = utils.RegionSize
	TLABChunkSize  = utils.TLABChunkSize
	SizeClassCount = utils.SizeClassCount
	HeaderSize     = utils.HeaderSize
)

func New(size uint64) (*Arena, error) { return Open(size, -1) }
func Open(size uint64, fd int) (*Arena, error) {
	return utils.NewArenaWithOptions(size, fd, utils.ArenaOptions{NonMoving: true})
}
func GetSizeClass(size uint64) (int, uint64) { return utils.GetExtendedSizeClass(size) }

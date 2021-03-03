// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build amd64 !darwin,arm64 mips64 mips64le ppc64 ppc64le s390x

// See mpagealloc_32bit.go for why darwin/arm64 is excluded here.

package runtime

import "unsafe"

const (
	// The number of levels in the radix tree.
	summaryLevels = 5

	// Constants for testing.
	pageAlloc32Bit = 0
	pageAlloc64Bit = 1
)

// levelBits is the number of bits in the radix for a given level in the super summary
// structure.
//
// The sum of all the entries of levelBits should equal heapAddrBits.
var levelBits = [summaryLevels]uint{
	summaryL0Bits,
	summaryLevelBits,
	summaryLevelBits,
	summaryLevelBits,
	summaryLevelBits,
}

// levelShift is the number of bits to shift to acquire the radix for a given level in the super summary structure.
//
// With levelShift, one can compute the index of the summary at level l related to a pointer p by doing:
//   p >> levelShift[l]
var levelShift = [summaryLevels]uint{
	heapAddrBits - summaryL0Bits, // 34
	heapAddrBits - summaryL0Bits - 1*summaryLevelBits, // 31
	heapAddrBits - summaryL0Bits - 2*summaryLevelBits, // 28
	heapAddrBits - summaryL0Bits - 3*summaryLevelBits, // 25
	heapAddrBits - summaryL0Bits - 4*summaryLevelBits, // 22
}

// levelLogPages is log2 the maximum number of runtime pages in the address space a summary in the given level represents.
//
// The leaf level always represents exactly log2 of 1 chunk's worth of pages.
var levelLogPages = [summaryLevels]uint{
	logPallocChunkPages + 4*summaryLevelBits,
	logPallocChunkPages + 3*summaryLevelBits,
	logPallocChunkPages + 2*summaryLevelBits,
	logPallocChunkPages + 1*summaryLevelBits,
	logPallocChunkPages,
}

// sysInit performs architecture-dependent initialization of fields in pageAlloc.
// pageAlloc should be uninitialized except for sysStat if any runtime statistic should be updated.
func (s *pageAlloc) sysInit() {
	// Reserve memory for each level. This will get mapped in as R/W by setArenas.
	// 让咱们来算个账
	// go heap 最多能表示 1<< heapAddrBits 的内存，也就是 1<< 48 的内存，256TB。
	// 基数树的每一个 chunk (pallocData) 是长度为 8 的 int64 的数组，每一位表示一个 page，也就是 8*64(1<<9) 的位图，每一个 page 是 8192 (1<< 13)
	// 所以基数树初始化了 1 << (48-9-13 = 26) 个 chunk 用来表示所有的 page 使用情况。
	// 而基数树总共五层，最底层（第五层） 1<< 26 个 pallocSum，最底层的每一个子节点的 pallocSum 包含了一个 chunk 的信息，pallocSum 的 max 最多可以表示 1<< 22 个空闲的 page
	// 基数树每上升一层，对应层的子节点 pallocSum 则包含了 8 * 下层子节点的信息，也就是第四层的子节点 pallocSum 包含了 8*chunk 的信息，也就是 8*64*8(1<< 11) 个 page 的信息
	// 顺序推导到第一层，第一层的子节点 pallocSum 则包含了 8*64*8*8*8*8 (1<< 21) 个 page 的信息，而 pallocSum 的 max 最多可以表示 1<< 22 个空闲的 page，正好够用。
	// 第一层的每一个子节点的 pallocSum 包含了 1<< 21 个 page 的信息，page 本身是 8192 ( 1<< 13) 的大小，所以第一层初始化了 1 << 14(48-21-13) 个 pallocSum
	for l, shift := range levelShift {
		entries := 1 << (heapAddrBits - shift)

		// Reserve b bytes of memory anywhere in the address space.
		b := alignUp(uintptr(entries)*pallocSumBytes, physPageSize)
		r := sysReserve(nil, b)
		if r == nil {
			throw("failed to reserve page summary memory")
		}

		// Put this reservation into a slice.
		sl := notInHeapSlice{(*notInHeap)(r), 0, entries}
		s.summary[l] = *(*[]pallocSum)(unsafe.Pointer(&sl))
	}
}

// sysGrow performs architecture-dependent operations on heap
// growth for the page allocator, such as mapping in new memory
// for summaries. It also updates the length of the slices in
// s.summary.
//
// base is the base of the newly-added heap memory and limit is
// the first address past the end of the newly-added heap memory.
// Both must be aligned to pallocChunkBytes.
//
// The caller must update s.start and s.end after calling sysGrow.
func (s *pageAlloc) sysGrow(base, limit uintptr) {
	if base%pallocChunkBytes != 0 || limit%pallocChunkBytes != 0 {
		print("runtime: base = ", hex(base), ", limit = ", hex(limit), "\n")
		throw("sysGrow bounds not aligned to pallocChunkBytes")
	}

	// Walk up the radix tree and map summaries in as needed.
	cbase, climit := chunkBase(s.start), chunkBase(s.end)
	for l := len(s.summary) - 1; l >= 0; l-- {
		// Figure out what part of the summary array this new address space needs.
		// Note that we need to align the ranges to the block width (1<<levelBits[l]) at this level because the full block is needed to compute the summary for the next level.
		lo, hi := addrsToSummaryRange(l, base, limit)
		lo, hi = blockAlignSummaryRange(l, lo, hi)

		// Update the summary slices with a new upper-bound. This ensures
		// we get tight bounds checks on at least the top bound.
		//
		// We must do this regardless of whether we map new memory, because we
		// may be extending further into the mapped memory.
		if hi > len(s.summary[l]) {
			s.summary[l] = s.summary[l][:hi]
		}

		// Figure out what part of the summary array is already mapped.
		// If we're doing our first growth, just pass zero.
		// addrsToSummaryRange won't accept cbase == climit.
		var mlo, mhi int
		if s.start != 0 {
			mlo, mhi = addrsToSummaryRange(l, cbase, climit)
			mlo, mhi = blockAlignSummaryRange(l, mlo, mhi)
		}

		// Extend the mappings for this summary level.
		extendMappedRegion(
			unsafe.Pointer(&s.summary[l][0]),
			uintptr(mlo)*pallocSumBytes,
			uintptr(mhi)*pallocSumBytes,
			uintptr(lo)*pallocSumBytes,
			uintptr(hi)*pallocSumBytes,
			s.sysStat,
		)
	}
}

// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import (
	"unsafe"
)

// Don't split the stack as this function may be invoked without a valid G,
// which prevents us from allocating more stack.
// sysAlloc 从操作系统获取一大块已清零的内存，一般是 100 KB 或 1MB
// NOTE: sysAlloc 返回 OS 对齐的内存，但是对于堆分配器来说可能需要以更大的单位进行对齐。
// 因此 caller 需要小心地将 sysAlloc 获取到的内存重新进行对齐
//go:nosplit
func sysAlloc(n uintptr, sysStat *uint64) unsafe.Pointer {
	v, err := mmap(nil, n, _PROT_READ|_PROT_WRITE, _MAP_ANON|_MAP_PRIVATE, -1, 0)
	if err != 0 {
		return nil
	}
	mSysStatInc(sysStat, n)
	return v
}

func sysUnused(v unsafe.Pointer, n uintptr) {
	// MADV_FREE_REUSABLE is like MADV_FREE except it also propagates
	// accounting information about the process to task_info.
	madvise(v, n, _MADV_FREE_REUSABLE)
}

func sysUsed(v unsafe.Pointer, n uintptr) {
	// MADV_FREE_REUSE is necessary to keep the kernel's accounting
	// accurate. If called on any memory region that hasn't been
	// MADV_FREE_REUSABLE'd, it's a no-op.
	madvise(v, n, _MADV_FREE_REUSE)
}

func sysHugePage(v unsafe.Pointer, n uintptr) {
}

// Don't split the stack as this function may be invoked without a valid G,
// which prevents us from allocating more stack.
//go:nosplit
func sysFree(v unsafe.Pointer, n uintptr, sysStat *uint64) {
	mSysStatDec(sysStat, n)
	munmap(v, n)
}

func sysFault(v unsafe.Pointer, n uintptr) {
	mmap(v, n, _PROT_NONE, _MAP_ANON|_MAP_PRIVATE|_MAP_FIXED, -1, 0)
}

// sysReserve 会在不分配内存的情况下，保留一段地址空间。
// 如果传给它的指针是非 nil，意思是 caller 想保留这段地址，
// 但这种情况下，如果该段地址不可用时，sysReserve 依然可以选择另外的地址。
// 在一些操作系统的某些 case 下，sysReserve 化合简单地检查这段地址空间是否可用
// 同时并不会真地保留它。sysReserve 返回非空指针时，如果地址空间保留成功了
// 会将 *reserved 设置为 true，只是检查而未保留的话会设置为 false。
// NOTE: sysReserve 返回 系统对齐的内存，没有按堆分配器的更大对齐单位进行对齐，
// 所以 caller 需要将通过 sysAlloc 获取到的内存进行重对齐。
func sysReserve(v unsafe.Pointer, n uintptr) unsafe.Pointer {
	p, err := mmap(v, n, _PROT_NONE, _MAP_ANON|_MAP_PRIVATE, -1, 0)
	if err != 0 {
		return nil
	}
	return p
}

const _ENOMEM = 12

// sysMap 将之前保留的地址空间映射好以进行使用。
// 如果地址空间确实被保留的话，reserved 参数会为 true。只 check 的话是 false。
func sysMap(v unsafe.Pointer, n uintptr, sysStat *uint64) {
	mSysStatInc(sysStat, n)

	p, err := mmap(v, n, _PROT_READ|_PROT_WRITE, _MAP_ANON|_MAP_FIXED|_MAP_PRIVATE, -1, 0)
	if err == _ENOMEM {
		throw("runtime: out of memory")
	}
	if p != v || err != 0 {
		throw("runtime: cannot map pages in arena address space")
	}
}

package utils

import "sync"

const MaxPoolSliceCap = 131072

type Pool[T any] struct {
	p sync.Pool
}

func NewPool[T any](newFn func() T) Pool[T] {
	return Pool[T]{
		p: sync.Pool{
			New: func() any {
				return newFn()
			},
		},
	}
}

func (p *Pool[T]) Get() T {
	return p.p.Get().(T)
}

func (p *Pool[T]) Put(x T) {
	p.p.Put(x)
}

type SlicePool[T any] struct {
	p sync.Pool
}

func NewSlicePool[T any](newFn func() *[]T) SlicePool[T] {
	return SlicePool[T]{
		p: sync.Pool{
			New: func() any {
				return newFn()
			},
		},
	}
}

func (p *SlicePool[T]) Get() *[]T {
	return p.p.Get().(*[]T)
}

func (p *SlicePool[T]) Put(s *[]T) {
	if cap(*s) <= MaxPoolSliceCap {
		p.p.Put(s)
	}
}
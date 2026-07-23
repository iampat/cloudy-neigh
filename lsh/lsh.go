package lsh

import (
	"math/rand"

	"github.com/iampat/cloudy-neigh/vector"
)

func NewLSH42(size int, dim int) *Lsh {
	rnd := rand.New(rand.NewSource(42))
	return NewLSH(size, dim, rnd)
}

func NewLSH(size int, dim int, rnd *rand.Rand) *Lsh {
	lsh := Lsh{}
	for idx := 0; idx < size; idx++ {
		lsh.Matrix = append(lsh.Matrix, vector.NewRandomVec(dim, rnd))
	}
	return &lsh
}

type Lsh struct {
	Matrix []*vector.Vector32 `json:"matrix"`
}

func (h *Lsh) Hash(v *vector.Vector32) string {
	hash := ""
	for _, h := range h.Matrix {
		b := "0"
		if vector.Dot(v, h) > 0 {
			b = "1"
		}
		hash += b
	}
	return hash
}

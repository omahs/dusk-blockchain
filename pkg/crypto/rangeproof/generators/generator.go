package generator

import (
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/crypto/ristretto"
)

// This package will generate the generators for the pedersens and the bulletproof

type Generator struct {
	data  []byte
	Bases []ristretto.Point
}

// New will generate a generator which
// will use data to generate `n` points
func New(data []byte) *Generator {
	return &Generator{
		data:  data,
		Bases: []ristretto.Point{},
	}
}

//Clear will clear all of the Bases
// but leave the counter as is
func (g *Generator) Clear() {
	g.Bases = []ristretto.Point{}
}

// Iterate will generate a new point using
// the previous point's bytes as a seed or the original
// nonce data, if no previous point is available
func (g *Generator) Iterate() ristretto.Point {

	p := ristretto.Point{}

	if len(g.Bases) == 0 {
		p.Derive(g.data)
		return p
	}

	prevPoint := g.Bases[len(g.Bases)-1]
	p.Derive(prevPoint.Bytes())

	return p
}

// Compute will generate num amount of points, which will act as point generators
// using the initial data.
func (g *Generator) Compute(num uint32) {

	for i := uint32(0); i < num; i++ {
		g.Bases = append(g.Bases, g.Iterate())
	}

}

// Bloom filter implementation
package filter

import "hash/fnv"

type BloomFilter struct {
	bitset []bool
	size   uint32
	hashes uint8
}

func New(size uint32, hashes uint8) *BloomFilter {
	return &BloomFilter{
		bitset: make([]bool, size),
		size:   size,
		hashes: hashes,
	}
}

//in-memory
func (b *BloomFilter) Add(key []byte) {
	h1, h2 := hash(key)
	for i := uint8(0); i < b.hashes; i++ {
		idx := (h1 + uint32(i)*h2) % b.size

		b.bitset[idx] = true
	}
}

// bloom filter is false positive
func (b *BloomFilter) MightContain(key []byte) bool {
	h1, h2 := hash(key)

	for i := uint8(0); i < b.hashes; i++ {
		idx := (h1 + uint32(i)*h2) % b.size
		if !b.bitset[idx] {
			return false
		}
	}

	return true
}

func hash(data []byte) (uint32, uint32) {
	h := fnv.New64a()
	h.Write(data)

	sum := h.Sum64()

	return uint32(sum), uint32(sum >> 32)
}

package gen

import (
	"bytes"
	"crypto/aes"
	"fmt"
	"github.com/tjim/smpcc/runtime/bit"
	"github.com/tjim/smpcc/runtime/gc"
	basegen "github.com/tjim/smpcc/runtime/gc/gen"
	"github.com/tjim/smpcc/runtime/ot"
)

type vm struct {
	io basegen.IO
}

func NewVM(io basegen.IO, id gc.ConcurrentId) basegen.VM {
	// id only to have the same type as other gc back ends (yaor, gax, gaxr)
	return vm{io}
}

func slot(keys []gc.Key) int {
	result := 0
	for i := 0; i < len(keys); i++ {
		key := keys[i]
		result *= 2
		result += int(key[0] % 2)
	}
	return result
}

func encrypt(keys []gc.Key, result []byte) []byte {
	for i := 0; i < len(keys); i++ {
		result = gc.Encrypt(keys[i], result)
	}
	return result
}

func decrypt(keys []gc.Key, ciphertext []byte) []byte {
	result := ciphertext
	for i := len(keys); i > 0; i-- {
		result = gc.Decrypt(keys[i-1], result)
	}
	return result
}

func encrypt_slot(t gc.GarbledTable, plaintext []byte, keys ...gc.Key) {
	t[slot(keys)] = encrypt(keys, plaintext)
}

func Decrypt(t gc.GarbledTable, keys ...gc.Key) []byte {
	return decrypt(keys, t[slot(keys)])
}

const (
	KEY_SIZE = aes.BlockSize
)

var key0 gc.Key    // The XOR random constant
var const0 gc.Wire // A wire for a constant 0 bit with unbounded fanout
var const1 gc.Wire // A wire for a constant 1 bit with unbounded fanout

func init_key0() {
	if key0 != nil {
		return
	}
	key0 = make([]byte, KEY_SIZE)
	gc.GenKey(key0) // least significant bit is random...
	key0[0] |= 1    // ...force it to 1
}

func init_constants(io basegen.IO) {
	if const0 == nil {
		const0 = genWire()
		const1 = genWire()
		io.SendK(const0[0])
		io.SendK(const1[1])
	}
}

func reset() {
	key0 = nil
	const0 = nil
	const1 = nil
}

// Generates two keys of size KEY_SIZE and returns the pair
func genWire() gc.Wire {
	init_key0()
	k0 := make([]byte, KEY_SIZE)
	gc.GenKey(k0)
	k1 := gc.XorKey(k0, key0)
	return []gc.Key{k0, k1}
}

// Generates an array of wires. A wire is a pair of keys.
func genWires(size int) []gc.Wire {
	if size <= 0 {
		panic("genWires with request <= 0")
	}
	res := make([]gc.Wire, size)
	for i := 0; i < size; i++ {
		res[i] = genWire()
	}
	return res
}

/* http://www.llvm.org/docs/LangRef.html */

func (y vm) And(a, b []gc.Wire) []gc.Wire {
	if len(a) != len(b) {
		panic("Wire mismatch in gen.And()")
	}
	result := make([]gc.Wire, len(a))
	for i := 0; i < len(a); i++ {
		w := genWire()
		result[i] = w
		t := make([]gc.Ciphertext, 4)
		encrypt_slot(t, w[0], a[i][0], b[i][0])
		encrypt_slot(t, w[0], a[i][0], b[i][1])
		encrypt_slot(t, w[0], a[i][1], b[i][0])
		encrypt_slot(t, w[1], a[i][1], b[i][1])
		y.io.SendT(t)
	}
	return result
}

func (y vm) Or(a, b []gc.Wire) []gc.Wire {
	if len(a) != len(b) {
		panic("Wire mismatch in gen.Or()")
	}
	result := make([]gc.Wire, len(a))
	for i := 0; i < len(a); i++ {
		w := genWire()
		result[i] = w
		t := make([]gc.Ciphertext, 4)
		encrypt_slot(t, w[0], a[i][0], b[i][0])
		encrypt_slot(t, w[1], a[i][0], b[i][1])
		encrypt_slot(t, w[1], a[i][1], b[i][0])
		encrypt_slot(t, w[1], a[i][1], b[i][1])
		y.io.SendT(t)
	}
	return result
}

func (y vm) Xor(a, b []gc.Wire) []gc.Wire {
	if len(a) != len(b) {
		panic("Xor(): mismatch")
	}
	result := make([]gc.Wire, len(a))
	for i := 0; i < len(a); i++ {
		k0 := gc.XorKey(a[i][0], b[i][0])
		k1 := gc.XorKey(a[i][0], b[i][1])
		result[i] = []gc.Key{k0, k1}
	}
	return result
}

func (y vm) True() []gc.Wire {
	init_constants(y.io)
	return []gc.Wire{const1}
}

func (y vm) False() []gc.Wire {
	init_constants(y.io)
	return []gc.Wire{const0}
}

/* Reveal to party 0 = gen */
func (y vm) RevealTo0(a []gc.Wire) []bool {
	result := make([]bool, len(a))
	for i := 0; i < len(a); i++ {
		bit := resolveKey(a[i], y.io.RecvK2())
		if bit == 0 {
			result[i] = false
		} else {
			result[i] = true
		}
	}
	return result
}

/* Reveal to party 1 = eval */
func (y vm) RevealTo1(a []gc.Wire) {
	for i := 0; i < len(a); i++ {
		t := make([]gc.Ciphertext, 2)
		w := genWire()
		w[0][0] = 0
		w[1][0] = 1
		encrypt_slot(t, w[0], a[i][0])
		encrypt_slot(t, w[1], a[i][1])
		y.io.SendT(t)
	}
}

func (y vm) ShareTo0(bits int) []gc.Wire {
	a := make([]gc.Wire, bits)
	for i := 0; i < len(a); i++ {
		w := genWire()
		a[i] = w
		y.io.Send(ot.Message(w[0]), ot.Message(w[1]))
	}
	return a
}

func (y vm) ShareTo1(a uint64, bits int) []gc.Wire {
	if bits > 64 {
		panic("BT: bits > 64")
	}
	result := make([]gc.Wire, bits)
	for i := 0; i < bits; i++ {
		w := genWire()
		result[i] = w
		if (a>>uint(i))%2 == 0 {
			y.io.SendK(w[0])
		} else {
			y.io.SendK(w[1])
		}
	}
	return result
}

// Random generates random bits.
func (y vm) Random(bits int) []gc.Wire {
	if bits < 1 {
		panic("Random: bits < 1")
	}
	result := make([]gc.Wire, bits)
	numBytes := bits / 8
	if bits%8 != 0 {
		numBytes++
	}
	random := make([]byte, numBytes)
	gc.GenKey(random)
	for i, _ := range result {
		w := genWire()
		result[i] = w
		switch bit.GetBit(random, i) {
		case 0:
			y.io.Send(ot.Message(w[0]), ot.Message(w[1]))
		default:
			y.io.Send(ot.Message(w[1]), ot.Message(w[0]))
		}
	}
	return result
}

func resolveKey(w gc.Wire, k gc.Key) int {
	if bytes.Equal(k, w[0]) {
		return 0
	} else if bytes.Equal(k, w[1]) {
		return 1
	} else {
		panic(fmt.Sprintf("resolveKey(): key and wire mismatch\nKey: %v\nWire: %v\n", k, w))
	}
	panic("unreachable")
}

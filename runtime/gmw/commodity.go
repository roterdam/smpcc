package gmw

import (
	"crypto/cipher"
	"github.com/tjim/smpcc/runtime/bit"
	"github.com/tjim/smpcc/runtime/ot"
)

type CommodityServerState struct {
	RandomStreams []cipher.Stream
	CorrectionCh  chan []byte
}

func NewCommodityServerState(partyCh []chan []byte) *CommodityServerState {
	numParties := len(partyCh)
	if numParties == 0 {
		return nil
	}
	s := &CommodityServerState{make([]cipher.Stream, numParties), partyCh[0]}
	for i, _ := range s.RandomStreams {
		seed := ot.RandomBytes(ot.SeedBytes)
		s.RandomStreams[i] = ot.NewPRG(seed)
		partyCh[i] <- seed
	}
	// close channels to all but the distinguished (first) party
	partyCh = partyCh[1:]
	for _, ch := range partyCh {
		close(ch)
	}
	return s
}

// A multiplication triple (a,b,c) satisfies (a AND b) = c
// In GMW each party is supposed to have an XOR share of a triple
// In the commodity model each party has a random number generator known to the server
// Each party (but one) generates its share of (a,b,c) randomly
// A designated party generates a random share of (a,b,c) but also receives a **correction** from the commodity server
// The server can calculate the correction because it knows the randomness of each party
// The correction is XORed by the designated party with their c component, resulting in a true multiplication triple
func (s *CommodityServerState) TripleCorrection() {
	numBytes := NUM_TRIPLES * 4
	a := make([]byte, numBytes)
	b := make([]byte, numBytes)
	c := make([]byte, numBytes)
	// calculate and combine the shares of all the parties
	for _, v := range s.RandomStreams {
		v.XORKeyStream(a, a)
		v.XORKeyStream(b, b)
		v.XORKeyStream(c, c)
	}
	// calculate the desired value for c
	desired := AndBytes(a, b)
	// desired = (c XOR correction) so correction = (desired XOR c)
	s.CorrectionCh <- ot.XorBytes(desired, c)
}

// A mask triple (a,B,C) satisfies (a AND B) = C
// Here a is a bit (represented as a byte 0 or 1) and C = 0 if a = 0, and C = B if a = 1
// In GMW each party is supposed to have an XOR share of a mask triple (the share of a is a byte, 0 or 1)
// In the commodity model each party has a random number generator known to the server
// Each party (but one) generates its share of (a,B,C) randomly
// A designated party generates a random share of (a,B,C) but also receives a **correction** from the commodity server
// The server can calculate the correction because it knows the randomness of each party
// The correction is XORed by the designated party with their c component, resulting in a true mask triple
//
// NB we use the same CommodityServerState for mask triples and multiplication triples
func (s *CommodityServerState) MaskTripleCorrection(numTriples, numBytesTriple int) {
	A := make([]byte, numTriples/8)
	B := make([]byte, numTriples*numBytesTriple)
	C := make([]byte, numTriples*numBytesTriple)
	for _, v := range s.RandomStreams {
		v.XORKeyStream(A, A)
		v.XORKeyStream(B, B)
		v.XORKeyStream(C, C)
	}
	correction := make([]byte, numTriples*numBytesTriple)
	for j := 0; j < numTriples; j++ {
		low := j * numBytesTriple
		high := low + numBytesTriple
		if bit.GetBit(A, j) == 1 {
			ot.XorBytesTo(B[low:high], C[low:high], correction[low:high]) // B is desired output
		} else { // the bit is 0
			copy(correction[low:high], C[low:high]) // all 0 is desired output
		}
	}
	s.CorrectionCh <- correction
}

type CommodityRequest interface {
	RequestTripleCorrection()
	RequestMaskTripleCorrection(numTriples, numBytesTriple int)
}

type CommodityClientState struct {
	RandomStream cipher.Stream
	CorrectionCh chan []byte
	Requester    CommodityRequest
}

func NewCommodityClientState(ch chan []byte, requester CommodityRequest) *CommodityClientState {
	s := &CommodityClientState{nil, ch, requester}
	return s
}

func InitCommodityClientState(s *CommodityClientState, distinguished bool) {
	seed := <-s.CorrectionCh
	s.RandomStream = ot.NewPRG(seed)
	if !distinguished {
		s.CorrectionCh = nil
		// we do not close the channel, in NATS this will be a subscription channel, we must unsubscribe as well, so caller closes
	}
}

func (s *CommodityClientState) triple32() []Triple {
	numBytes := NUM_TRIPLES * 4
	a := make([]byte, numBytes)
	b := make([]byte, numBytes)
	c := make([]byte, numBytes)
	s.RandomStream.XORKeyStream(a, a)
	s.RandomStream.XORKeyStream(b, b)
	s.RandomStream.XORKeyStream(c, c)
	if s.CorrectionCh != nil {
		s.Requester.RequestTripleCorrection()
		correction := <-s.CorrectionCh
		c = ot.XorBytes(c, correction)
	}
	result := make([]Triple, NUM_TRIPLES)
	for i := range result {
		result[i] = Triple{combine(a[:4]), combine(b[:4]), combine(c[:4])}
		a = a[4:]
		b = b[4:]
		c = c[4:]
	}
	return result
}

func (s *CommodityClientState) maskTriple(numTriples, numBytesTriple int) []MaskTriple {
	a := make([]byte, numTriples/8)
	b := make([]byte, numTriples*numBytesTriple)
	c := make([]byte, numTriples*numBytesTriple)
	s.RandomStream.XORKeyStream(a, a)
	s.RandomStream.XORKeyStream(b, b)
	s.RandomStream.XORKeyStream(c, c)
	if s.CorrectionCh != nil {
		s.Requester.RequestMaskTripleCorrection(numTriples, numBytesTriple)
		correction := <-s.CorrectionCh
		c = ot.XorBytes(c, correction)
	}
	result := make([]MaskTriple, numTriples)
	for i := range result {
		result[i] = MaskTriple{bit.GetBit(a, i), b[:numBytesTriple], c[:numBytesTriple]}
		b = b[numBytesTriple:]
		c = c[numBytesTriple:]
	}
	return result
}

package gc

import (
	//	"log"

	"math/big"

	"github.com/tjim/smpcc/runtime/ot"
)

type Genio interface {
	ot.Sender
	SendT(t GarbledTable)
	SendK(t Key)
	RecvK2() Key
}

type Evalio interface {
	ot.Receiver
	RecvT() GarbledTable
	RecvK() Key
	SendK2(t Key)
}

// **********************************************
type Chanio struct {
	Tchan   chan GarbledTable `fatchan:"request"`
	Kchan   chan Key          `fatchan:"request"`
	Kchan2  chan Key          `fatchan:"reply"`
	OtChans ot.OTChans
}

type GenX struct {
	channels *Chanio
	otSender ot.Sender
}

type EvalX struct {
	channels *Chanio
	otRecvr  ot.Receiver
}

func NewGenX(io *Chanio) *GenX {
	result := &GenX{
		io,
		ot.NewOTChansSender(&io.OtChans),
	}
	return result
}

func NewEvalX(io *Chanio) *EvalX {
	result := &EvalX{
		io,
		ot.NewOTChansReceiver(&io.OtChans),
	}
	return result
}

func (io *GenX) SendT(x GarbledTable) {
	io.channels.Tchan <- x
}

func (io *GenX) SendK(x Key) {
	io.channels.Kchan <- x
}

func (io *EvalX) SendK2(x Key) {
	io.channels.Kchan2 <- x
}

func (io *EvalX) RecvT() GarbledTable {
	result := <-io.channels.Tchan
	return result
}

func (io *EvalX) RecvK() Key {
	result := <-io.channels.Kchan
	return result
}

func (io *GenX) RecvK2() Key {
	result := <-io.channels.Kchan2
	return result
}

func (io *GenX) Send(m0, m1 ot.Message) {
	io.otSender.Send(m0, m1)
}

func (io *EvalX) Receive(s ot.Selector) ot.Message {
	return io.otRecvr.Receive(s)
}

func NewChanio() (io *Chanio) {
	io = &Chanio{
		make(chan GarbledTable, 50),
		make(chan Key, 50),
		make(chan Key, 50),
		ot.OTChans{
			make(chan ot.PublicKey, 100),
			make(chan big.Int, 100),
			make(chan ot.HashedElGamalCiph, 100),

			make(chan []byte, 100),
			make(chan ot.Selector, 100),
			make(chan ot.NPReceiverParams, 1),
		},
	}
	return io
}

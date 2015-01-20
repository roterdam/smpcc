package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/gob"
	"fmt"
	"github.com/apcera/nats"
	"github.com/tjim/smpcc/runtime/gmw"
	"github.com/tjim/smpcc/runtime/vickrey"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/ssh/terminal"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"os/signal"
)

func MarshalPublicKey(c *[32]byte) string {
	return fmt.Sprintf("%x", *c)
}

func UnmarshalPublicKey(s string) *[32]byte {
	if len(s) != 64 {
		panic("Malformed public key (wrong length)")
	}
	for _, v := range s {
		switch v {
		default:
			panic("Malformed public key (not lowercase hexidecimal)")
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f':
		}
	}
	result := new([32]byte)
	fmt.Sscanf(s, "%x", result)
	return result
}

type Room struct {
	Room string
}

type Party struct {
	Nick string
	Key  string
}

// messages from clients to secretary
type JoinRequest struct {
	Party
}

type LeaveRequest struct {
	Party
}

type StartRequest struct {
	Party
}

// messages from secretary to clients
type Message struct {
	Party
	Message string
}

type Members struct {
	Parties []Party
}

func Init() {
	gob.Register(JoinRequest{})
	gob.Register(LeaveRequest{})
	gob.Register(StartRequest{})
	gob.Register(Message{})
	gob.Register(Members{})
}

type RoomState struct {
	Sub     *nats.Subscription
	Members []Party
	Hash    []byte
}

func (st RoomState) indexOfParty(p Party) (int, error) {
	for i, v := range st.Members {
		if v == p {
			return i, nil
		}
	}
	return 0, nil
}

func encode(p interface{}) []byte {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(&p)
	if err != nil {
		log.Fatal("encode:", err)
	}
	return buf.Bytes()
}

var MyPrivateKey *[32]byte
var MyPublicKey string
var MyParty Party
var MyRooms map[string]*RoomState
var MyRoom string
var MyNick string

func initialize() {
	Init()
	rawPrivateKey, rawPublicKey, _ := box.GenerateKey(rand.Reader)
	MyPrivateKey = rawPrivateKey
	MyPublicKey = MarshalPublicKey(rawPublicKey)
	MyNick = "AnonymousCoward"
	MyParty = Party{MyNick, MyPublicKey}
	MyRooms = make(map[string]*RoomState)
}

func changeNick(nick string) {
	MyNick = nick
	MyParty = Party{MyNick, MyPublicKey}
}

func escape(s string) []byte {
	in := []byte(s)
	var out []byte
	for _, b := range in {
		switch {
		case b == '\t':
			out = append(out, ' ')
		case b == '\r':
			continue
		case b == '\n':
			out = append(out, '\n')
		case b < 32:
			out = append(out, '?')
		default:
			out = append(out, b)
		}
	}
	return out
}

func Tprintf(term *terminal.Terminal, format string, v ...interface{}) {
	term.Write(escape(fmt.Sprintf(format, v...)))
}

func client() {
	initialize()
	oldState, err := terminal.MakeRaw(0)
	if err != nil {
		panic(err.Error())
	}
	defer terminal.Restore(0, oldState)

	sigChan := make(chan os.Signal)
	signal.Notify(sigChan) // sadly this is useless in raw mode. cbreak mode might help but not supported by terminal package
	go func() {
		s := <-sigChan
		panic(fmt.Sprintf("Signal: %v", s))
	}()

	term := terminal.NewTerminal(os.Stdin, "> ")
	Tprintf(term, "Greetings, %s!\n", MyNick)
	Tprintf(term, "Commands:\n")
	Tprintf(term, "join foo      (join the chatroom named foo)\n")
	Tprintf(term, "leave foo     (leave the chatroom named foo)\n")
	Tprintf(term, "nick foo      (change your nickname to foo)\n")
	Tprintf(term, "members       (list the parties in the current room)\n")
	Tprintf(term, "^D            (buh-bye)\n")
	Tprintf(term, "anything else (send anything else to your current chatroom)\n")

	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		panic("unable to connect to NATS server")
	}

	for {
		line, err := term.ReadLine()
		if err != nil {
			log.Println("Error!", err)
			break
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		switch words[0] {
		case "nick":
			if len(words) == 2 {
				changeNick(words[1])
				Tprintf(term, "Your nickname is now %s\n", MyNick)

			} else {
				Tprintf(term, "Nicknames must be one word\n")
			}
		case "join":
			if len(words) == 2 {
				room := words[1]
				MyRoom = room
				term.SetPrompt(fmt.Sprintf("[%s]> ", MyRoom))
				_, ok := MyRooms[room]
				if !ok {
					joinTerm(nc, term, room)
				}
			} else {
				Tprintf(term, "Join what room?\n")
			}
		case "rooms":
			for room, _ := range MyRooms {
				Tprintf(term, "%s\n", room)
			}
		case "leave":
			words = words[1:]
			for _, room := range words {
				if st, ok := MyRooms[room]; ok {
					delete(MyRooms, room)
					err = st.Sub.Unsubscribe()
					checkError(err)
					err = nc.Publish(fmt.Sprintf("secretary.%s", room), encode(LeaveRequest{MyParty}))
					checkError(err)
					if MyRoom == room {
						if len(MyRooms) == 0 {
							MyRoom = ""
							term.SetPrompt(fmt.Sprintf("> "))
						} else {
							for r, _ := range MyRooms {
								MyRoom = r
								term.SetPrompt(fmt.Sprintf("[%s]> ", MyRoom))
								break
							}
						}
					}
				} else {
					Tprintf(term, "Not a member of %s\n", room)
				}
			}
		case "members":
			if MyRoom == "" {
				Tprintf(term, "You must join a room before you can see the members of the room\n")
			} else {
				st := MyRooms[MyRoom]
				for _, member := range st.Members {
					Tprintf(term, "%s (%s)\n", member.Key, member.Nick)
				}
				Tprintf(term, "Hash: %x\n", st.Hash)
			}
		case "run":
			if MyRoom == "" {
				Tprintf(term, "You must join a room before you can start a computation\n")
			} else {
				Tprintf(term, "Starting computation\n")
				session(nc, words[1:])
			}
		default:
			if MyRoom != "" {
				msg := strings.TrimSpace(line)
				err = nc.Publish(MyRoom, encode(Message{MyParty, msg}))
				checkError(err)
			} else {
				Tprintf(term, "You must join a room first\n")
			}
		}
	}
}

func session(nc *nats.Conn, args []string) {
	inputs := make([]uint32, len(args))
	for i, v := range args {
		input := 0
		fmt.Sscanf(v, "%d", &input)
		inputs[i] = uint32(input)
	}
	Handle := vickrey.Handle
	numBlocks := Handle.NumBlocks
	id := -1
	rm := MyRoom
	st := MyRooms[rm]
	for i, v := range st.Members {
		if v == MyParty {
			id = i
			break
		}
	}
	if id == -1 {
		panic("Non-member trying to start a computation in a room")
	}
	ec, _ := nats.NewEncodedConn(nc, "gob")

	numParties := len(st.Members)
	io := gmw.NewPeerIO(numBlocks, numParties, id)
	io.Inputs = inputs
	blocks := io.Blocks
	numBlocks = len(blocks) // increased by one by NewPeerIo

	xs := make([]*gmw.PerNodePair, numParties)
	for p := 0; p < numParties; p++ {
		if p == id {
			continue
		}
		x := gmw.NewPerNodePair(io)
		xs[p] = x
		if io.Leads(p) {
			// leader is server
			// that means it receives in the fatchan sense
			// also it is going to act as sender for the base OT
			ec.BindSendChan(fmt.Sprintf("%s-%d-%d-ParamChan", rm, id, p), x.ParamChan)
			ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-NpRecvPk", rm, p, id), x.NpRecvPk)
			ec.BindSendChan(fmt.Sprintf("%s-%d-%d-NpSendEncs", rm, id, p), x.NpSendEncs)
			for i := 0; i < numBlocks; i++ {
				ec.BindSendChan(fmt.Sprintf("%s-%d-%d-%d", rm, id, p, i), x.BlockChans[i].SAS.Rwchannel)
				ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-%d", rm, p, id, i), x.BlockChans[i].CAS.Rwchannel)
				ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-%d-CAS-S2R", rm, p, id, i), x.BlockChans[i].CAS.S2R)
				ec.BindSendChan(fmt.Sprintf("%s-%d-%d-%d-CAS-R2S", rm, id, p, i), x.BlockChans[i].CAS.R2S)
				ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-%d-SAS-R2S", rm, p, id, i), x.BlockChans[i].SAS.R2S)
				ec.BindSendChan(fmt.Sprintf("%s-%d-%d-%d-SAS-S2R", rm, id, p, i), x.BlockChans[i].SAS.S2R)
			}
		} else {
			ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-ParamChan", rm, p, id), x.ParamChan)
			ec.BindSendChan(fmt.Sprintf("%s-%d-%d-NpRecvPk", rm, id, p), x.NpRecvPk)
			ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-NpSendEncs", rm, p, id), x.NpSendEncs)
			for i := 0; i < numBlocks; i++ {
				ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-%d", rm, p, id, i), x.BlockChans[i].SAS.Rwchannel)
				ec.BindSendChan(fmt.Sprintf("%s-%d-%d-%d", rm, id, p, i), x.BlockChans[i].CAS.Rwchannel)
				ec.BindSendChan(fmt.Sprintf("%s-%d-%d-%d-CAS-S2R", rm, id, p, i), x.BlockChans[i].CAS.S2R)
				ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-%d-CAS-R2S", rm, p, id, i), x.BlockChans[i].CAS.R2S)
				ec.BindSendChan(fmt.Sprintf("%s-%d-%d-%d-SAS-R2S", rm, id, p, i), x.BlockChans[i].SAS.R2S)
				ec.BindRecvChan(fmt.Sprintf("%s-%d-%d-%d-SAS-S2R", rm, p, id, i), x.BlockChans[i].SAS.S2R)
			}
		}
	}
	// tell secretary we want to start the computation
	okStart := make(chan bool)
	ec.BindRecvChan(fmt.Sprintf("%s.secretary.okStart", rm), okStart)
	err := nc.Publish(fmt.Sprintf("secretary.%s", rm), encode(StartRequest{MyParty}))
	checkError(err)
	log.Println("Waiting...")
	if !(<-okStart) {
		panic("Computation failed to start")
	}
	log.Println("Got the all clear")

	done := make(chan bool)
	log.Printf("There are %d parties and I am party %d\n", numParties, id)
	for p := 0; p < numParties; p++ {
		if p == id {
			continue
		}
		log.Printf("Working on party %d\n", p)
		x := xs[p]
		if io.Leads(p) {
			log.Println("Starting server side for", p)
			go gmw.ServerSideIOSetup(io, p, x, done) 
		} else {
			log.Println("Starting client side for", p)
			go gmw.ClientSideIOSetup(io, p, x, false, done)
		}
	}
	for p := 0; p < numParties; p++ {
		if p == id {
			continue
		}
		<-done
	}
	log.Println("Done setup")

	// TODO: start computation
	numBlocks = Handle.NumBlocks // make sure we have the right numBlocks
	// copy io.blocks[1:] to make an []Io; []BlockIO is not []Io
	x := make([]gmw.Io, numBlocks)
	for j := 0; j < numBlocks; j++ {
		x[j] = io.Blocks[j+1]
	}
	go Handle.Main(io.Blocks[0], x)
	<-Handle.Done
}

func joinTerm(nc *nats.Conn, term *terminal.Terminal, rm string) {
	err := nc.Publish(fmt.Sprintf("secretary.%s", rm), encode(JoinRequest{MyParty}))
	checkError(err)
	sub, err := nc.Subscribe(rm, func(m *nats.Msg) {
		dec := gob.NewDecoder(bytes.NewBuffer(m.Data))
		var p interface{}
		err := dec.Decode(&p)
		if err != nil {
			log.Fatal("decode:", err)
		}
		switch r := p.(type) {
		case Message:
			if r.Party != MyParty {
				Tprintf(term, "[%s]: %s -- (%s)\n", rm, r.Message, r.Party.Nick)
			}
		case Members:
			st := MyRooms[rm]
			st.Members = r.Parties
			h := sha1.New()
			for _, member := range st.Members {
				io.WriteString(h, member.Key)
			}
			st.Hash = h.Sum(nil)
		}
	})
	checkError(err)
	MyRooms[rm] = &RoomState{sub, nil, nil} // needs lock
}

func secretary() {
	log.Println("starting secretary")
	initialize()
	changeNick("ChatAdministrator")
	rooms := make(map[string]bool)
	members := make(map[string](map[Party]bool))
	starters := make(map[string](map[Party]bool))
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		panic("unable to connect to NATS server")
	}
	nc.Subscribe("secretary.>", func(m *nats.Msg) {
		dec := gob.NewDecoder(bytes.NewBuffer(m.Data))
		var p interface{}
		err := dec.Decode(&p)
		if err != nil {
			log.Fatal("decode:", err)
		}
		room := m.Subject[len("secretary."):]
		if !rooms[room] {
			rooms[room] = true
		}
		switch r := p.(type) {
		case StartRequest:
			log.Println(r.Party, "asking to run in room", room)
			if _, ok := members[room]; !ok {
				log.Println("Warning: run request for empty room", room)
				return
			}
			if _, ok := starters[room]; !ok {
				starters[room] = make(map[Party]bool)
				for k, _ := range members[room] {
					starters[room][k] = false
				}
			}
			starters[room][r.Party] = true
			for k, v := range starters[room] {
				if !v {
					log.Println("Still waiting for", k, "to run the computation in", room)
					return 
				}
			}
			log.Println("Starting computation in", room)
			ec, _ := nats.NewEncodedConn(nc, "gob")
			okStart := make(chan bool)
			ec.BindSendChan(fmt.Sprintf("%s.secretary.okStart", room), okStart)
			okStart <- true
			log.Println("Should be started")
		case LeaveRequest:
			delete(members[room], r.Party)
			_ = nc.Publish(room, encode(Message{MyParty, fmt.Sprintf("%s has left the room", r.Party.Nick)}))
			log.Println("Leave", room, r)
		case JoinRequest:
			if _, ok := members[room]; !ok {
				members[room] = make(map[Party]bool)
			}
			members[room][r.Party] = true
			_ = nc.Publish(room, encode(Message{MyParty, fmt.Sprintf("%s has joined %s", r.Party.Nick, room)}))
			log.Println("Join", room, r)
			numMembers := len(members[room])
			parties := make([]Party, 0, numMembers)
			for party, _ := range members[room] {
				parties = append(parties, party)
			}
			_ = nc.Publish(room, encode(Members{parties}))
			log.Println("Members", room, members[room])
		case Message:
			log.Println("Message", r.Message)
		}
	})
	runtime.Goexit()
}

func checkError(err error) {
	if err != nil {
		log.Fatal("Error:", err)
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "secretary" {
		secretary()
	} else {
		client()
	}
}

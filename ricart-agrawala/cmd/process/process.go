package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/ricart-agrawala/lib/consts"
	"github.com/ricart-agrawala/lib/message"
)

type Process struct {
	id   int
	addr *net.UDPAddr
	conn *net.UDPConn
}

type State string

const (
	Wanted   State = "WANTED"
	Held     State = "HELD"
	Released State = "RELEASED"
)

type Clock struct {
	timestamp int
	mutex     *sync.Mutex
}

type HeadProcess struct {
	id    int
	clock Clock
	state State

	addr *net.UDPAddr
	recv *net.UDPConn

	links []*Process

	sharedResource *net.UDPConn

	replyQueue []int
	responded  int
}

func NewHeadProcess(id int) *HeadProcess {
	return &HeadProcess{id: id, clock: Clock{timestamp: 0, mutex: &sync.Mutex{}}, state: Released, responded: 0, replyQueue: make([]int, 0)}
}

func (p *HeadProcess) InitializeConnections(addresses []string) error {
	if err := p.initializeReceiver(addresses[p.id-1]); err != nil {
		return err
	}

	if err := p.initializeProcessLinks(addresses); err != nil {
		return err
	}

	if err := p.initializeSharedResourceLink(consts.LocalIp + consts.SharedResourcePort); err != nil {
		return err
	}

	return nil
}

func (p *HeadProcess) initializeReceiver(address string) error {
	recvAddr, err := net.ResolveUDPAddr(consts.UDPProtocol, address)
	if err != nil {
		return err
	}
	p.addr = recvAddr

	recv, err := net.ListenUDP(consts.UDPProtocol, recvAddr)
	if err != nil {
		return err
	}

	p.recv = recv
	return nil
}

func (p *HeadProcess) initializeProcessLinks(addresses []string) error {
	var links []*Process

	for i, address := range addresses {
		procId := i + 1
		if procId == p.id {
			continue
		}

		process := &Process{
			id: procId,
		}

		addr, err := net.ResolveUDPAddr(consts.UDPProtocol, address)
		if err != nil {
			return err
		}
		process.addr = addr

		conn, err := net.DialUDP(consts.UDPProtocol, nil, addr)
		if err != nil {
			return err
		}
		process.conn = conn

		links = append(links, process)
	}

	p.links = links
	return nil
}

func (p *HeadProcess) initializeSharedResourceLink(address string) error {
	sharedResourceAddr, err := net.ResolveUDPAddr(consts.UDPProtocol, address)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP(consts.UDPProtocol, nil, sharedResourceAddr)
	if err != nil {
		return err
	}

	p.sharedResource = conn
	return nil
}

func (p *HeadProcess) GetLinkWithId(id int) (*Process, error) {
	for _, link := range p.links {
		if link.id == id {
			return link, nil
		}
	}
	return &Process{}, errors.New("no link with id " + strconv.Itoa(id))
}

func (p *HeadProcess) SendMessage(msg *message.Message, conn *net.UDPConn) error {
	buf := msg.EncodeToBytes()
	_, err := conn.Write(buf)
	return err
}

func (p *HeadProcess) ListenForMessages() {
	buf := make([]byte, 1024)

	for {
		n, _, err := p.recv.ReadFromUDP(buf)
		msg := message.DecodeToMessage(buf[0:n])

		go p.HandleMessage(msg)

		if err != nil {
			fmt.Println("Error:", err)
		}
	}
}

func (p *HeadProcess) ListenForInput() {
	inputChannel := make(chan string)

	// Read input
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			input, _, err := reader.ReadLine()
			if err != nil {
				fmt.Println("Error:", err.Error())
			}
			inputChannel <- string(input)
		}
	}()

	for input := range inputChannel {
		if input == "x" {
			err := p.RequestSharedResource()
			if err != nil {
				fmt.Println("Error:", err.Error())
			}
		} else {
			fmt.Println("Invalid input")
		}
	}
}

func (p *HeadProcess) AlterState(nextState State) {
	p.state = nextState
	fmt.Println("State:", p.state+"; Clock:", p.clock.timestamp)
}

func (p *HeadProcess) IncrementClock(increment int) {
	p.clock.mutex.Lock()
	p.clock.timestamp = p.clock.timestamp + increment
	p.clock.mutex.Unlock()
}

func (p *HeadProcess) MatchAndIncrementClock(timestamp int) {
	increment := 1
	if dif := timestamp - p.clock.timestamp; dif > 0 {
		increment += dif
	}
	p.IncrementClock(increment)
}

func (p *HeadProcess) RequestSharedResource() error {
	if p.state != Released {
		return errors.New("input ignored, process not in released state")
	}

	p.IncrementClock(1)
	p.AlterState(Wanted)
	msg := message.NewMessage(p.id, p.clock.timestamp, message.Request)
	for _, link := range p.links {
		go p.SendMessage(msg, link.conn)
	}
	return nil
}

func (p *HeadProcess) ReplyToRequest(recipientId int) error {
	reply := message.NewMessage(p.id, p.clock.timestamp, message.Reply)
	link, err := p.GetLinkWithId(recipientId)
	if err != nil {
		return err
	}
	go p.SendMessage(reply, link.conn)
	return nil
}

func (p *HeadProcess) AcquireSharedResource() {
	p.IncrementClock(1)
	p.AlterState(Held)
	msg := message.NewMessage(p.id, p.clock.timestamp, message.Acquire)
	go p.SendMessage(msg, p.sharedResource)

	fmt.Println("Acquired CS; Clock:", p.clock.timestamp)
	time.Sleep(consts.CSTimeout)
}

func (p *HeadProcess) ReleaseSharedResource() error {
	p.IncrementClock(1)
	fmt.Println("Released CS; Clock:", p.clock.timestamp)
	p.AlterState(Released)
	for _, id := range p.replyQueue {
		if err := p.ReplyToRequest(id); err != nil {
			return err
		}
	}
	p.replyQueue = make([]int, 0)
	return nil
}

func (p *HeadProcess) HandleMessage(msg *message.Message) error {
	switch msg.Type {
	case message.Request:
		p.MatchAndIncrementClock(msg.Clock)

		if p.state == Held || (p.state == Wanted && msg.Clock < p.clock.timestamp) {
			p.replyQueue = append(p.replyQueue, msg.From)
		} else if err := p.ReplyToRequest(msg.From); err != nil {
			return err
		}

	case message.Reply:
		p.MatchAndIncrementClock(msg.Clock)
		p.responded++

		if p.responded == len(p.links) { // Received replies from every process
			p.responded = 0

			p.AcquireSharedResource()

			if err := p.ReleaseSharedResource(); err != nil {
				return err
			}
		}
	}
	return nil
}

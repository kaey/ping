// Package ping implements ICMP ping functions.
package ping

import (
	"errors"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Errors
var (
	ErrInvalidAddr = errors.New("ping: invalid address")
	ErrClosed      = errors.New("ping: closed")
	ErrTimeout     = errors.New("ping: timeout")
)

// Result is a result of a single ICMP roundtrip.
type Result struct {
	From     string        // TODO: fill this field with address we received ICMP packet from.
	Type     ipv4.ICMPType // TODO: fill this field with received ICMP packet type.
	Sent     time.Time
	Received time.Time
}

// Pinger implements ping functions.
type Pinger struct {
	conn    *icmp.PacketConn
	proto   int
	stop    chan struct{}
	limiter chan struct{}

	mu    sync.Mutex
	idMap []chan int
}

// New returns new pinger with specified local socket address.
func New(laddr string) (*Pinger, error) {
	c, err := icmp.ListenPacket("ip4:icmp", laddr)
	if err != nil {
		return nil, err
	}

	// TODO: add ipv6 support.
	p := &Pinger{
		conn:    c,
		proto:   ipv4.ICMPTypeEcho.Protocol(),
		limiter: make(chan struct{}, 200), // TODO: make limiter size configurable?
		stop:    make(chan struct{}),
		idMap:   make([]chan int, 65536),
	}

	go p.limitRoutine()
	go p.readRoutine()

	return p, nil
}

// LimitRoutine manages limit of sent ICMP echo requests.
// This is needed, because host can't accept replies fast enough.
func (p *Pinger) limitRoutine() {
	// TODO: make limiter refill interval configurable?
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			for i := 0; i < cap(p.limiter); i++ {
				p.limiter <- struct{}{}
			}
		case <-p.stop:
			return
		}
	}
}

// Close stops all pending requests and closes socket.
func (p *Pinger) Close() error {
	close(p.stop)
	return p.conn.Close()
}

// Once sends single echo packet to raddr and returns round trip time.
func (p *Pinger) Once(raddr string, deadline time.Duration) (rtt time.Duration, err error) {
	// TODO: do something with deadline, because of p.limiter, write goroutine may
	// not have a chance to send its ICMP echo packet.
	result, err := p.Ping(raddr, deadline, 1, time.Millisecond)
	if err != nil {
		return 0, err
	}

	return result[0].Received.Sub(result[0].Sent), nil
}

// Ping sends count echo requests to raddr with specified delay.
func (p *Pinger) Ping(raddr string, deadline time.Duration, count int, delay time.Duration) ([]Result, error) {
	// We start sequence num at 1, not 0, so increment count by 1.
	count++

	ip := net.ParseIP(raddr)
	if ip == nil {
		return nil, ErrInvalidAddr
	}

	ticker := time.NewTicker(delay)
	defer ticker.Stop()

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	var (
		timeoutChan = timer.C
		tickChan    = ticker.C
		recvChan    = make(chan int)
		result      = make([]Result, count)
	)

	id := p.newID(recvChan)
	defer p.freeID(id)

	// Main loop.
	sent, received := 1, 1
	for {
		if sent == count && received == count {
			return result[1:], nil
		}
		select {
		case seq := <-recvChan:
			if seq == 0 || seq >= count {
				log.Printf("ping: unexpected sequence num %v, raddr: %v, id %v", seq, raddr, id)
				continue
			}
			result[seq].Received = time.Now()
			received++
		case <-p.stop:
			return result, ErrClosed
		case <-timeoutChan:
			return result, ErrTimeout
		case now := <-tickChan:
			go p.write(ip, id, sent)
			result[sent].Sent = now
			sent++
			if sent == count {
				tickChan = nil
			}
		}
	}
}

// Write sends echo request to host ip with specified id and seq.
// It should be run in a separate goroutine.
func (p *Pinger) write(ip net.IP, id, seq int) {
	select {
	case <-p.stop:
		return
	case <-p.limiter:
	}

	wm := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:  id,
			Seq: seq,
		},
	}

	wb, err := wm.Marshal(nil)
	if err != nil {
		log.Println("ping: write: Marshal:", err)
		return
	}

	if _, err := p.conn.WriteTo(wb, &net.IPAddr{IP: ip}); err != nil {
		log.Println("ping: write: WriteTo:", err)
		return
	}
}

// BufPool is a packet buffer for readRoutine.
var bufPool = &sync.Pool{
	New: func() interface{} {
		return make([]byte, 10000)
	},
}

// ReadRoutine reads packets from socket and calls handleReply
// in a separate goroutine for each one.
func (p *Pinger) readRoutine() {
	for {
		select {
		case <-p.stop:
			return
		default:
			buf := bufPool.Get().([]byte)
			n, addr, err := p.conn.ReadFrom(buf)
			if err != nil {
				log.Println("ping: readRoutine: ReadFrom:", err)
				bufPool.Put(buf)
				continue
			}

			go p.handleReply(addr, n, buf)
		}
	}
}

// HandleReply parses packet and sends event to main loop.
func (p *Pinger) handleReply(addr net.Addr, n int, buf []byte) {
	defer bufPool.Put(buf)
	msg, err := icmp.ParseMessage(p.proto, buf[:n])
	if err != nil {
		log.Println("ping: handleReply: ParseMessage:", err)
		return
	}

	// TODO: correctly parse icmp types
	switch msg.Type {
	case ipv4.ICMPTypeEchoReply:
		// handle normally
	case ipv4.ICMPTypeDestinationUnreachable, ipv4.ICMPTypeTimeExceeded:
		// ignore for now
		return
	default:
		log.Printf("ping: handleReply: addr: %v, unexpected message type: %v", addr, msg.Type)
		return
	}

	id := msg.Body.(*icmp.Echo).ID
	seq := msg.Body.(*icmp.Echo).Seq
	p.mu.Lock()
	ch := p.idMap[id]
	p.mu.Unlock()
	if ch != nil {
		// TODO: there is a data race between this send and select in freeID().
		ch <- seq
	}
}

// NewID returns free ID to use in ICMP echo and assigns recvChan to this ID,
// which is used by handleReply().
func (p *Pinger) newID(recvChan chan int) int {
	for {
		p.mu.Lock()
		for i := 0; i < 65536; i++ {
			if p.idMap[i] != nil {
				continue
			}
			p.idMap[i] = recvChan
			p.mu.Unlock()
			return i
		}
		p.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
}

// FreeID frees used ID.
func (p *Pinger) freeID(id int) {
	p.mu.Lock()
	ch := p.idMap[id]
	if ch != nil {
		p.idMap[id] = nil
		select {
		case <-ch:
		default:
		}
	}
	p.mu.Unlock()
}

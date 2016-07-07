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

type Result struct {
	From     string // TODO: add from address
	Sent     time.Time
	Received time.Time
}

type Pinger struct {
	conn  *icmp.PacketConn
	proto int
	stop  chan struct{}

	mu    sync.Mutex
	idMap []chan int
}

func New(laddr string) (*Pinger, error) {
	c, err := icmp.ListenPacket("ip4:icmp", laddr)
	if err != nil {
		return nil, err
	}

	p := &Pinger{
		conn:  c,
		proto: ipv4.ICMPTypeEcho.Protocol(),
		stop:  make(chan struct{}),
		idMap: make([]chan int, 65536),
	}

	go p.readRoutine()

	return p, nil
}

func (p *Pinger) Close() error {
	close(p.stop)
	return p.conn.Close()
}

func (p *Pinger) Once(raddr string, timeout time.Duration) (rtt time.Duration, err error) {
	result, err := p.Ping(raddr, timeout, 1, time.Millisecond)
	if err != nil {
		return 0, err
	}

	return result[0].Received.Sub(result[0].Sent), nil
}

func (p *Pinger) Multiple(raddr string, timeout time.Duration, count int, delay time.Duration) (rtt time.Duration, lost int, err error) {
	result, err := p.Ping(raddr, timeout, count, delay)
	if err != nil {
		return 0, 0, err
	}

	_ = result // FIXME

	return 0, 0, nil
}

func (p *Pinger) Ping(raddr string, deadline time.Duration, count int, delay time.Duration) ([]Result, error) {
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
		result      = make([]Result, count+1)
	)

	id := p.newID(recvChan)
	defer p.freeID(id)

	for sent, received := 0, 0; sent < count || received < count; {
		select {
		case seq := <-recvChan:
			result[seq].Received = time.Now()
			received++
		case <-p.stop:
			return result, ErrClosed
		case <-timeoutChan:
			return result, ErrTimeout
		case now := <-tickChan:
			seq := sent + 1
			if err := p.write(ip, id, seq); err != nil {
				return result, err
			}
			result[seq].Sent = now
			sent++
			if sent == count {
				tickChan = nil
			}
		}
	}

	return result[1:], nil
}

func (p *Pinger) write(ip net.IP, id, seq int) error {
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
		return err
	}

	if _, err := p.conn.WriteTo(wb, &net.IPAddr{IP: ip}); err != nil {
		return err
	}

	return nil
}

func (p *Pinger) readRoutine() {
	buf := make([]byte, 10000)
	for {
		select {
		case <-p.stop:
			return
		default:
			n, _, err := p.conn.ReadFrom(buf)
			if err != nil {
				log.Println("ping.ReadFrom:", err)
				continue
			}

			msg, err := icmp.ParseMessage(p.proto, buf[:n])
			if err != nil {
				log.Println("icmp.ParseMessage:", err)
				continue
			}

			// FIXME: parse other icmp types
			if msg.Type != ipv4.ICMPTypeEchoReply {
				continue
			}

			id := msg.Body.(*icmp.Echo).ID
			seq := msg.Body.(*icmp.Echo).Seq
			p.mu.Lock()
			ch := p.idMap[id]
			p.mu.Unlock()
			if ch != nil {
				ch <- seq
			}
		}
	}
}

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

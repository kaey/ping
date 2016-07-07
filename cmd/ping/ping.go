package main

import (
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaey/ping"
)

func main() {
	p, err := ping.New("0.0.0.0")
	if err != nil {
		log.Fatalln(err)
	}

	count := 0
	if len(os.Args) > 1 {
		c, err := strconv.ParseInt(os.Args[1], 10, 64)
		if err != nil {
			log.Fatalln(err)
		}
		count = int(c)
	}

	data, err := ioutil.ReadFile("addr")
	if err != nil {
		log.Fatalln(err)
	}

	wg := new(sync.WaitGroup)
	lines := strings.Split(string(data), "\n")
	result := make([]time.Duration, 0, len(lines))
	mu := sync.Mutex{}
	ticker := time.NewTicker(1 * time.Millisecond) // TODO: move to lib
	for i, line := range lines {
		if i >= count {
			break
		}
		<-ticker.C
		wg.Add(1)
		addr := strings.TrimSpace(line)
		go func() {
			defer wg.Done()

			rtt, _ := p.Once(addr, 10*time.Second)

			mu.Lock()
			result = append(result, rtt)
			mu.Unlock()

			/*result, err := p.Ping(addr, 10*time.Second, 5, 5*time.Millisecond)
			if err != nil {
				log.Printf("%q: %v", addr, err)
				return
			}

			for i, r := range result {
				log.Printf("Addr: %v, Seq: %v, sent: %v, recv: %v\n", addr, i, r.Sent, r.Received)
			}*/
		}()
	}
	wg.Wait()

	ok := 0
	nok := 0
	for _, rtt := range result {
		if rtt > 0 {
			ok++
		} else {
			nok++
		}
	}

	log.Printf("ok: %v, nok: %v, total: %v", ok, nok, len(result))
}

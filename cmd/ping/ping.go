// Copyright 2016 Konstantin Kulikov. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kaey/ping"
)

var (
	count   = flag.Int("count", 0, "Send ping to only first count addresses.")
	srcAddr = flag.String("src", "0.0.0.0", "Socket address")
)

func main() {
	flag.Parse()
	p, err := ping.New(*srcAddr)
	if err != nil {
		log.Fatalln(err)
	}

	r := bufio.NewReader(os.Stdin)
	wg := new(sync.WaitGroup)
	var result []time.Duration
	mu := sync.Mutex{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalln("read stdin:", err)
		}

		wg.Add(1)
		addr := strings.TrimSpace(line)
		go func() {
			defer wg.Done()

			rtt, _ := p.Once(addr, 100*time.Second)

			mu.Lock()
			result = append(result, rtt)
			mu.Unlock()
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

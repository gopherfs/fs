// So.... Groupcache while being brilliant is also fairly annoying. Instead of having
// semantics where you can only do HTTPOptions once per mux and RegisterPeerPicker
// once per Groupcache, you can only do this once... period. Or my favorite, panic().
// This prevents you from doing neat things like running different groupcache's on
// different ports, or in this case for testing the groupcache's peering picker
// you designed works. So this is here to create a binary I can startup with my test
// and test this works.  This is going to be way easier than trying to negotiate a CL
// to change this with Go team.  I simply don't have the patience at this point for
// the kind of crap that is required to get Go team's attention from outside Google.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gopherfs/fs/io/cache/groupcache/peerpicker"
)

var (
	ip   = flag.String("ip", "", "The IP of the peer we are looking for")
	peer = flag.String("peer", "", "The IP of the peer we are looking for")
	port = flag.Int("port", 0, "The port to run on")
)

func main() {
	flag.Parse()

	local := net.ParseIP(*ip)
	if !local.IsLoopback() {
		panic("--ip must be a loopback")
	}
	if local.To4() == nil {
		panic("--ip must be IPv4")
	}

	lan, err := peerpicker.New(*port, peerpicker.WithSettings(local, nil, nil))
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	if err := waitForPeer(ctx, "http://"+*peer, lan); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func waitForPeer(ctx context.Context, httpPeer string, lan *peerpicker.LAN) error {
	doneFile := filepath.Join(os.TempDir(), *ip)
	quitFile := filepath.Join(os.TempDir(), *peer)

	found := false
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("%s was not found before timeout", httpPeer)
		}

		peers := lan.Peers()
		if !found {
			for _, p := range peers {
				if p == httpPeer {
					found = true
					log.Println("wrote doneFile: ", doneFile)
					if err := os.WriteFile(doneFile, []byte("done"), 0644); err != nil {
						return err
					}
				}
			}
		} else {
			if _, err := os.Stat(quitFile); err == nil {
				log.Println("see quitFile: ", quitFile)
				return nil
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}

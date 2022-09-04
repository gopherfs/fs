/*
Package peerpicker provides a groupcache.PeerPicker that utilizes a LAN peer discovery
mechanism and sets up the groupcache to use the HTTPPool for communication between
nodes.

Example:

	picker, err := peerpicker.New(7586)
	if err != nil {
		// Do something
	}

	fsys, err := groupcache.New(picker)
	if err != nil {
		// Do something
	}
*/
package peerpicker

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/golang/groupcache"
	jsfs "github.com/gopherfs/fs"
	"github.com/schollz/peerdiscovery"
)

// IsPeer determines if a discovered peer is a peer for our groupcache. Each peer will
// send a peer.Payload of [peer key]:[iam address], such as "groupcache:127.0.01".
// IsPeer() should return true if the key matches and return the iam address. We get
// the iam address from the message instead of the IP that sent it because discovery
// happens on all IPs configured for a device.
type IsPeer func(peer peerdiscovery.Discovered) (bool, string)

// LAN provides a groupcache.PeerPicker utilizing schollz peerdiscovery.
type LAN struct {
	*groupcache.HTTPPool

	settings []peerdiscovery.Settings
	payload  []byte
	peerKey  []byte
	iam      string
	isPeer   IsPeer
	closed   chan struct{}
	serv     *http.Server

	peers      atomic.Value //[]string
	setPeersCh chan []peerdiscovery.Discovered

	logger jsfs.Logger
}

// Option is optional settings for the New() constructor.
type Option func(l *LAN) error

// WithSettings allows passing your own settings for peer discovery. If not specified
// this will go with our own default values for ipv4 and ipv6 (if setup). We default
// to port 9999. iam in the net.IP that you wish to broadcast as. This defaults to
// an IPv6 address on hosts with IPv6.
func WithSettings(iam net.IP, settings []peerdiscovery.Settings, isPeer IsPeer) Option {
	return func(l *LAN) error {
		if len(iam) == 0 {
			return fmt.Errorf("iam must be a valid IP")
		}
		l.settings = settings
		l.isPeer = isPeer
		l.iam = iam.String()

		return nil
	}
}

// WithLogger specifies a logger for us to use.
func WithLogger(logger jsfs.Logger) Option {
	return func(l *LAN) error {
		l.logger = logger
		return nil
	}
}

// New creates a New *LAN instance listening on 'port' for groupcache connections.
func New(port int, options ...Option) (*LAN, error) {
	l := &LAN{
		logger:     jsfs.DefaultLogger{},
		setPeersCh: make(chan []peerdiscovery.Discovered, 1),
	}

	for _, o := range options {
		if err := o(l); err != nil {
			return nil, err
		}
	}
	l.defaultSettings()

	l.HTTPPool = groupcache.NewHTTPPoolOpts(
		"http://"+l.iam,
		&groupcache.HTTPPoolOptions{},
	)

	l.serv = &http.Server{
		Addr:           fmt.Sprintf("%s:%d", l.iam, port),
		Handler:        l.HTTPPool,
		ReadTimeout:    3 * time.Second,
		WriteTimeout:   3 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	go func() {
		l.logger.Println("groupcache peerpicker serving on: ", l.serv.Addr)
		if err := l.serv.ListenAndServe(); err != nil {
			l.logger.Printf("groupcache peerpicker stopped(%s)", l.serv.Addr)
		}
	}()
	go l.discovery()

	return l, nil
}

// Close stops peer discovery and shuts down the http server used with groupcache.
func (l *LAN) Close() {
	close(l.closed)
	l.serv.Shutdown(context.Background())
}

// Peers retrieves the list of peers. This is only useful for debugging and monitoring.
// Changing a peer in this list may result in unintended behavior.
func (l *LAN) Peers() []string {
	p := l.peers.Load()
	if p == nil {
		return nil
	}
	return p.([]string)
}

func (l *LAN) defaultSettings() error {
	const (
		timeLimit = 2 * time.Second
		delay     = 500 * time.Millisecond
	)

	var ipv4, ipv6 bool

	if l.iam != "" {
		ip := net.ParseIP(l.iam)
		if ip.To4() != nil {
			ipv4 = true
		} else {
			ipv6 = true
		}
	} else {
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return err
		}
		for _, addr := range addrs {
			ip := addr.(*net.IPNet).IP
			if ip.IsLoopback() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}

			if ip.To4() == nil {
				ipv6 = true
				if l.iam == "" {
					l.iam = ip.String()
				}
				continue
			}
			ipv4 = true
			if !ipv6 && l.iam == "" {
				l.iam = ip.String()
			}
		}
	}

	if l.payload == nil {
		l.payload = []byte(fmt.Sprintf(`groupcache:%s`, l.iam))
	}
	l.peerKey = bytes.Split(l.payload, []byte(":"))[0]
	if l.isPeer == nil {
		l.isPeer = l.defaultIsPeer
	}

	if l.settings == nil {
		if ipv4 {
			l.settings = append(
				l.settings,
				peerdiscovery.Settings{
					TimeLimit: timeLimit,
					IPVersion: peerdiscovery.IPv4,
					Delay:     delay,
					Payload:   l.payload,
					AllowSelf: true,
				},
			)
		}
		if ipv6 {
			l.settings = append(
				l.settings,
				peerdiscovery.Settings{
					Limit:     -1,
					TimeLimit: timeLimit,
					IPVersion: peerdiscovery.IPv6,
					Delay:     delay,
					Payload:   l.payload,
					AllowSelf: true,
				},
			)
		}
	}
	if len(l.settings) == 0 {
		return fmt.Errorf("neither IPv4 or IPv6 exists on the machine")
	}
	return nil
}

func (l *LAN) defaultIsPeer(peer peerdiscovery.Discovered) (bool, string) {
	entries := bytes.Split(peer.Payload, []byte(":"))
	if len(entries) < 2 {
		return false, ""
	}
	if bytes.Equal(peer.Payload, l.peerKey) {
		return false, ""
	}
	return true, string(entries[1])
}

func (l *LAN) discovery() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	go l.setPeers()
	defer close(l.setPeersCh)

	for {

		select {
		case <-l.closed:
			return
		case <-tick.C:
		}

		log.Println("did peer discovery")
		peers, err := peerdiscovery.Discover(l.settings...)
		if err != nil {
			l.logger.Printf("groupcache peerdiscovery: %s", err)
			continue
		}

		l.setPeersCh <- peers
	}
}

func (l *LAN) setPeers() {
	for peers := range l.setPeersCh {
		log.Println("setPeers")
		peerList := []string{}

		for _, peer := range peers {
			if isPeer, peerAddr := l.isPeer(peer); isPeer {
				if peerAddr == l.iam {
					continue
				}
				peerList = append(peerList, "http://"+peerAddr)
			} else {
				log.Printf("saw peer I discounted: %s, %s", peer.Address, string(peer.Payload))
			}
		}
		log.Println("peerList is: ", peerList)

		peerList = sort.StringSlice(peerList)
		var prevPeers []string

		if i := l.peers.Load(); i != nil {
			prevPeers = i.([]string)
		}

		// If we don't have the same length of peers, we know the peer list is different.
		if len(peerList) != len(prevPeers) {
			l.peers.Store(peerList)
			l.HTTPPool.Set(peerList...)
			return
		}

		// If any peer at an index is different, update our set of peers.
		for i, addr := range peerList {
			if prevPeers[i] != addr {
				l.peers.Store(peerList)
				l.HTTPPool.Set(peerList...)
				break
			}
		}
	}
}

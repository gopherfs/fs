package cache

import (
	"regexp"
	"testing"
	"path/filepath"
	"os"
	"time"
	"bytes"

	osfs "github.com/gopherfs/fs/io/os"
	"github.com/gopherfs/fs/io/cache"
	"github.com/gopherfs/fs/io/cache/disk"
	"github.com/gopherfs/fs/io/cache/redis"
	"github.com/google/uuid"

)

func TestETOE(t *testing.T) {
	content := []byte(`hello world`)

	permLoc := filepath.Join(os.TempDir(), uuid.New().String())

	// Create our long term storage and create a SubFS rooted at permLoc.
	permStore, err := osfs.New()
	if err != nil {
		panic(err)
	}
	if err := permStore.Mkdir(permLoc, 0744); err != nil {
		panic(err)	
	}
	defer os.RemoveAll(permLoc)

	sub, err := permStore.Sub(permLoc)
	if err != nil {
		panic(err)
	}
	permStore = sub.(*osfs.FS)

	
	if err := permStore.Mkdir("dir", 0744); err != nil {
		panic(err)
	}
	// Write a file that can be retrieved by our cache layers.
	if err := permStore.WriteFile("dir/file", content, 0644); err != nil {
		panic(err)
	}

	// Create our disk cache, which stores temporary disk cached files.
	// In real life, our permStore would be off system, but for testing we
	// have two disk layers.
	diskFS, err := disk.New("", disk.WithExpireCheck(5 * time.Second), disk.WithExpireFiles(10 * time.Second))
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(diskFS.Location())

	// This sets up our first layer disk cache that pulls from our disk cache and them permanent
	// storage.
	diskCache, err := cache.New(diskFS, permStore)
	if err != nil {
		panic(err)
	}

	// Now make a Redis cache that we hit first.
	redisFS, err := redis.New(
		redis.Args{Addr: "127.0.0.1:6379"},
		redis.WithWriteFileOFOptions(
			regexp.MustCompile(`.*`),
			redis.ExpireFiles(2 * time.Second),
		),
	)
	if err != nil {
		panic(err)
	}

	// This sets up our second layer network cache that pulls from redis and then the disk
	// cache we setup earlier.
	networkCache, err := cache.New(redisFS, diskCache)
	if err != nil {
		panic(err)
	}

	fetch("first fill", networkCache, content, "*os.FS", t)
	time.Sleep(1 * time.Second)

	fetch("second fill", networkCache, content, "*redis.FS", t)
	time.Sleep(3 * time.Second)

	fetch("third fill", networkCache, content, "*disk.FS", t)
	time.Sleep(1 * time.Second)

	fetch("fourth fill", networkCache, content, "*redis.FS", t)
	time.Sleep(11 * time.Second)

	fetch("fifth fill", networkCache, content, "*os.FS", t)

	// Allow disk writes to occur so we don't get a weird error like:
	// "invalid argument" because our cleanup runs when the fill goroutine hasn't occurred.
	time.Sleep(2 * time.Second) 
}

func fetch(desc string, c *cache.FS, expectContent []byte, expectFill string, t *testing.T) {
	got, err := c.ReadFile("dir/file")
	if err != nil {
		panic(err)
	}

	if bytes.Compare(got, expectContent) != 0 {
		t.Fatalf("ETOE: recieved file content %q, want %q", string(got), string(expectContent))
	}
	if expectFill != "" {
		if c.FilledBy != expectFill {
			t.Errorf("TestETOE(%s): got FilledBy: %s, want %s", desc, c.FilledBy, expectFill)
		}
	}
}

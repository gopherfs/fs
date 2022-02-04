package disk

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	jsfs "github.com/gopherfs/fs"

	"github.com/petar/GoLLRB/llrb"
)

type index struct {
	sync.Mutex

	logger    jsfs.Logger
	location  string
	olderThan time.Duration
	expires   *llrb.LLRB
	byName    map[string]expireKey
}

func newIndex(location string, logger jsfs.Logger, olderThan time.Duration) *index {
	return &index{
		logger:    logger,
		expires:   llrb.New(),
		location:  location,
		olderThan: olderThan,
		byName:    map[string]expireKey{},
	}
}

func (i *index) add(name string) error {
	i.Lock()
	defer i.Unlock()

	if _, ok := i.byName[name]; ok {
		return fmt.Errorf("key exists")
	}
	k := expireKey{Time: time.Now().Add(i.olderThan), name: name}
	i.byName[name] = k
	i.expires.InsertNoReplace(k)
	return nil
}

func (i *index) update(name string) error {
	i.Lock()
	defer i.Unlock()

	k, ok := i.byName[name]
	if !ok {
		return fmt.Errorf("key does not exists")
	}
	i.expires.Delete(k)

	k.Time = time.Now().Add(i.olderThan)
	i.byName[name] = k

	i.expires.InsertNoReplace(k)
	return nil
}

func (i *index) addOrUpdate(name string) {
	i.Lock()
	defer i.Unlock()

	k, ok := i.byName[name]
	if ok {
		i.expires.Delete(k)
		k.Time = time.Now().Add(i.olderThan)

	} else {
		k = expireKey{Time: time.Now().Add(i.olderThan), name: name}
	}
	i.byName[name] = k
	i.expires.InsertNoReplace(k)
}

func (i *index) deleteOld() {
	i.expires.AscendLessThan(
		expireKey{Time: time.Now().Add(-i.olderThan)},
		i.expireItem,
	)
}

func (i *index) expireItem(item llrb.Item) bool {
	ek := item.(expireKey)
	i.expires.Delete(ek)
	name := filepath.Join(i.location, nameTransform(ek.name))
	if err := os.Remove(name); err != nil {
		i.logger.Println("error removing file: ", err)
	}
	//log.Printf("Removing expired: %s(%s)", ek.name, name)
	return true
}

type expireKey struct {
	time.Time

	name string
}

func (e expireKey) Less(than llrb.Item) bool {
	return than.(expireKey).Before(e.Time)
}

package route

import (
	"sync"
	"sync/atomic"
	"time"

	"math/rand"
)

type Registry struct {
	sMap    map[uint64]*Pool
	rLock   sync.RWMutex
	rCond   sync.Cond
	rRev    uint64
	closed  bool
	initCtl sync.Once
}

func (self *Registry) init() {
	self.initCtl.Do(func() {
		self.sMap = make(map[uint64]*Pool)
		self.rCond.L = self.rLock.RLocker()
	})
}

func (self *Registry) touch() {
	self.rLock.Lock()
	atomic.AddUint64(&self.rRev, 1)
	self.rLock.Unlock()
	self.rCond.Broadcast()
}

func (self *Registry) push(id uint64, srv RouteInfo, action ...func()) {
	self.init()
	self.rLock.Lock()
	var closed = self.closed
	if self.sMap[id] == nil {
		var pool = new(Pool)
		pool.version = uint64(int(rand.Int31()))
		self.sMap[id] = pool
	}
	var servicePool = self.sMap[id]
	self.rLock.Unlock()
	if closed {
		return
	}

	servicePool.rLock.Lock()
	if servicePool.srvMap == nil {
		servicePool.srvMap = make(map[RouteInfo][]func())
	}
	servicePool.srvMap[srv] = append(servicePool.srvMap[srv], action...)
	servicePool.version++
	servicePool.rLock.Unlock()

	self.touch()
}

func (self *Registry) Push(id uint64, srv RouteInfo, action ...func()) {
	self.push(id, srv, action...)
}

func (self *Registry) Pop(id uint64, srv RouteInfo) {
	self.init()
	self.rLock.Lock()
	if self.sMap[id] == nil {
		self.rLock.Unlock()
		return
	}
	var servicePool = self.sMap[id]
	self.rLock.Unlock()

	servicePool.rLock.Lock()
	for _, actionList := range servicePool.srvMap {
		for _, action := range actionList {
			go action()
		}
	}
	delete(servicePool.srvMap, srv)
	servicePool.rLock.Unlock()
	self.rLock.Lock()
	if self.sMap[id] != nil && len(self.sMap[id].srvMap) == 0 {
		delete(self.sMap, id)
	}
	self.rLock.Unlock()
	self.touch()
}

func (self *Registry) DiscoverTimeout(r Selector, sname uint64, wait time.Duration) (srv RouteInfo, found bool) {
	self.init()
	//mpxLog.Println("RegistryStorage::DiscoverTimeout::enter", sname, wait)
	var started = time.Now()
	var now = started
	if wait == 0 {
		wait = time.Second
	}
	for started.Add(wait).After(now) {
		self.rLock.RLock()
		var servicePool = self.sMap[sname]
		servicePool = self.sMap[sname]
		self.rLock.RUnlock()

		if servicePool != nil {
			servicePool.rLock.RLock()
			var srvPool = make([]RouteInfo, 0, len(servicePool.srvMap))
			for srvL := range servicePool.srvMap {
				srvPool = append(srvPool, srvL)
			}
			if len(srvPool) > 0 {
				srv, found = srvPool[r.Select(srvPool)], true
			}
			servicePool.rLock.RUnlock()
		}

		if found {
			return
		}

		time.Sleep(time.Millisecond * 10)
		now = time.Now()
		continue
	}
	//mpxLog.Println("RegistryStorage::DiscoverTimeout::done", sname, srv == nil)
	return
}

func (self *Registry) Discover(r Selector, sname uint64) (srv RouteInfo, found bool) {
	self.init()
	return self.DiscoverTimeout(r, sname, time.Second)
}

func (self *Registry) Find(sname uint64, wait time.Duration) (servicePool *Pool) {
	self.init()
	self.rLock.RLock()
	servicePool = self.sMap[sname]
	self.rLock.RUnlock()
	return
}

func (self *Registry) Sync(other *Registry, onAdd, onDelete func(uint64, RouteInfo)) {
	self.init()
	other.init()
	var added, deleted []Pair

	var inSync = map[uint64]bool{}

	self.rLock.RLock()
	for service, sPool := range self.sMap {
		self.rLock.RUnlock()

		other.rLock.RLock()
		var sMap = other.sMap[service]
		other.rLock.RUnlock()

		sPool.rLock.Lock()
		var sRev = sPool.version
		var otherRev uint64
		if sMap != nil {
			otherRev = sMap.version
			if sRev == otherRev {
				inSync[service] = true
			} else {
				sPool.version = otherRev
			}
		}
		sPool.rLock.Unlock()

		self.rLock.RLock()
	}
	self.rLock.RUnlock()

	self.rLock.RLock()
	for service, sPool := range self.sMap {
		self.rLock.RUnlock()

		if _, ch := inSync[service]; !ch {
			sPool.rLock.RLock()
			for s := range sPool.srvMap {
				sPool.rLock.RUnlock()

				other.rLock.RLock()
				var sMap = other.sMap[service]
				other.rLock.RUnlock()
				if sMap == nil {
					deleted = append(deleted, Pair{service, s})
				} else {
					sMap.rLock.RLock()
					if _, found := sMap.srvMap[s]; !found {
						deleted = append(deleted, Pair{service, s})
					}
					sMap.rLock.RUnlock()
				}

				sPool.rLock.RLock()
			}
			sPool.rLock.RUnlock()
		}

		self.rLock.RLock()
	}
	self.rLock.RUnlock()

	other.rLock.RLock()
	for service, sPool := range other.sMap {
		other.rLock.RUnlock()

		if _, ch := inSync[service]; !ch {
			sPool.rLock.RLock()
			for s := range sPool.srvMap {
				sPool.rLock.RUnlock()

				self.rLock.RLock()
				var sMap = self.sMap[service]
				self.rLock.RUnlock()
				if sMap == nil {
					added = append(added, Pair{service, s})
				} else {
					sMap.rLock.RLock()
					if _, found := sMap.srvMap[s]; !found {
						added = append(added, Pair{service, s})
					}
					sMap.rLock.RUnlock()
				}

				sPool.rLock.RLock()
			}
			sPool.rLock.RUnlock()
		}

		other.rLock.RLock()
	}
	other.rLock.RUnlock()

	for _, add := range added {
		self.Push(add.K, add.V)
		if onAdd != nil {
			onAdd(add.K, add.V)
		}
	}
	for _, del := range deleted {
		self.Pop(del.K, del.V)
		if onDelete != nil {
			onDelete(del.K, del.V)
		}
	}
}

func (self *Registry) Iter() Iterator {
	self.init()
	return Iterator{self, 0}
}

func (self *Registry) Close() {
	var keep = true
	for keep {
		var last = atomic.LoadUint64(&self.rRev)
		var clean Registry
		self.Sync(&clean, nil, nil)

		self.rLock.Lock()
		var swapped = atomic.CompareAndSwapUint64(&self.rRev, last, last)
		if swapped {
			self.closed = true
		}
		keep = !self.closed
		self.rLock.Unlock()

	}
	self.touch()
}
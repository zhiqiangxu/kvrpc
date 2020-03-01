package document

import (
	"encoding/binary"
	"sync"

	"github.com/zhiqiangxu/kvrpc"
	"github.com/zhiqiangxu/kvrpc/provider"
)

// Sequence for allocating auto incrementing pk
type Sequence struct {
	sync.Mutex
	kvdb      kvrpc.KVDB
	key       []byte
	bandwidth uint64
	next      uint64
	leased    uint64
}

// NewSequence is ctor for Sequence
func NewSequence(kvdb kvrpc.KVDB, keyword []byte, bandwidth uint64) (s *Sequence, err error) {
	if len(keyword) == 0 {
		err = ErrEmptyKeywordForSequence
		return
	}

	if bandwidth == 0 {
		err = ErrZeroBandwidth
		return
	}

	s = &Sequence{kvdb: kvdb, key: sequenceKey(keyword), bandwidth: bandwidth}

	return
}

func (s *Sequence) updateLease() (err error) {
	txn := s.kvdb.NewTransaction(true)
	defer txn.Discard()

	val, _, err := txn.Get(s.key)
	switch {
	case err == provider.ErrKeyNotFound:
		s.next = 0
	case err != nil:
		return
	default:
		num := binary.BigEndian.Uint64(val)
		s.next = num
	}

	lease := s.next + s.bandwidth
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], lease)
	err = txn.Set(s.key, buf[:], nil)
	if err != nil {
		return
	}

	oldLease := s.leased
	s.leased = lease
	err = txn.Commit()
	if err != nil {
		s.leased = oldLease
	}
	return
}

// ReleaseRemaining for release the remaining sequence to avoid wasted integers.
func (s *Sequence) ReleaseRemaining() (err error) {
	s.Lock()
	defer s.Unlock()

	if s.leased == s.next {
		return
	}

	txn := s.kvdb.NewTransaction(true)
	defer txn.Discard()

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], s.next)
	err = txn.Set(s.key, buf[:], nil)
	if err != nil {
		return
	}

	s.leased = s.next
	return
}

// Next would return the next integer in the sequence, updating the lease by running a transaction
// if needed.
func (s *Sequence) Next() (val uint64, err error) {
	s.Lock()
	defer s.Unlock()

	if s.next >= s.leased {
		err = s.updateLease()
		if err != nil {
			return
		}
	}

	s.next++
	val = s.next
	return
}

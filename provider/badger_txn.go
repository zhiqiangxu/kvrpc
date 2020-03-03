package provider

import (
	"github.com/dgraph-io/badger"
	"github.com/zhiqiangxu/mondis"
)

// Txn is mondis wrapper for badger.Txn
type Txn badger.Txn

// Set for implement mondis.Txn
func (txn *Txn) Set(k, v []byte, meta *mondis.VMetaReq) (err error) {
	defer func() {
		if err == badger.ErrTxnTooBig {
			err = ErrTxnTooBig
		}
	}()

	if meta == nil {
		return (*badger.Txn)(txn).Set(k, v)
	}

	entry := badger.NewEntry(k, v).WithTTL(meta.TTL).WithMeta(meta.Tag)
	return (*badger.Txn)(txn).SetEntry(entry)
}

// Exists checks whether k exists
func (txn *Txn) Exists(k []byte) (exists bool, err error) {

	_, err = (*badger.Txn)(txn).Get(k)
	if err == badger.ErrKeyNotFound {
		err = nil
		return
	}
	if err != nil {
		return
	}

	exists = true
	return
}

// Get for implement mondis.Txn
func (txn *Txn) Get(k []byte) (v []byte, meta mondis.VMetaResp, err error) {

	item, err := (*badger.Txn)(txn).Get(k)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			err = ErrKeyNotFound
		}
		return
	}

	v, err = item.ValueCopy(nil)
	if err != nil {
		return
	}

	meta.ExpiresAt = item.ExpiresAt()
	meta.Tag = item.UserMeta()
	return
}

// Delete for implement mondis.Txn
func (txn *Txn) Delete(key []byte) (err error) {
	defer func() {
		if err == badger.ErrTxnTooBig {
			err = ErrTxnTooBig
		}
	}()

	err = (*badger.Txn)(txn).Delete(key)
	return
}

// Commit for implement mondis.Txn
func (txn *Txn) Commit() (err error) {
	err = (*badger.Txn)(txn).Commit()
	return
}

// Discard for implement mondis.Txn
func (txn *Txn) Discard() {
	(*badger.Txn)(txn).Discard()
}

// Scan over keys specified by option
func (txn *Txn) Scan(option mondis.ProviderScanOption, fn func(key []byte, value []byte, meta mondis.VMetaResp) bool) (err error) {
	err = scanByBadgerTxn((*badger.Txn)(txn), option, fn)

	return
}

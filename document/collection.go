package document

import (
	"errors"

	"github.com/zhiqiangxu/kvrpc"
	"github.com/zhiqiangxu/kvrpc/provider"
	"github.com/zhiqiangxu/util/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.uber.org/zap"
)

// Collection is like mongo collection
type Collection struct {
	db               *DB
	kvdb             kvrpc.KVDB
	cid              int64
	name             string
	documentSequence *Sequence
}

func newCollection(db *DB, cid int64, name string) (c *Collection) {

	kvdb := db.kvdb
	documentSequence, _ := NewSequence(kvdb, []byte(name), documentIDBandWidth)
	c = &Collection{db: db, kvdb: kvdb, cid: cid, name: name, documentSequence: documentSequence}
	return
}

// InsertOne for insert a document into collection
func (c *Collection) InsertOne(doc bson.M, txn kvrpc.ProviderTxn) (did int64, err error) {

	data, err := bson.Marshal(doc)
	if err != nil {
		return
	}

	// prologue start
	err = c.db.checkState()
	if err != nil {
		return
	}
	err = c.db.closer.Add(1)
	if err != nil {
		return
	}
	defer c.db.closer.Done()
	// prologue end

	udid, err := c.documentSequence.Next()
	if err != nil {
		return
	}

	docKey := GetCollectionDocumentKey(c.cid, int64(udid))

	{
		var oneshot bool
		if txn == nil {
			oneshot = true
			txn = c.kvdb.NewTransaction(true)
			defer txn.Discard()
		}
		err = txn.Set(docKey, data, nil)
		if err != nil {
			return
		}

		if oneshot {
			err = txn.Commit()
			if err != nil {
				return
			}
		}

		did = int64(udid)
	}

	return
}

var (
	// ErrDocNotFound when document not found
	ErrDocNotFound = errors.New("document not found")
)

// UpdateOne for update an existing document in collection
func (c *Collection) UpdateOne(did int64, doc bson.M, txn kvrpc.ProviderTxn) (updated bool, err error) {
	updated, _, err = c.updateOne(did, doc, false, txn)
	return
}

func (c *Collection) updateOne(did int64, doc bson.M, upsert bool, txn kvrpc.ProviderTxn) (updated, isNew bool, err error) {
	data, err := bson.Marshal(doc)
	if err != nil {
		return
	}

	// prologue start
	err = c.db.checkState()
	if err != nil {
		return
	}
	err = c.db.closer.Add(1)
	if err != nil {
		return
	}
	defer c.db.closer.Done()
	// prologue end

	docKey := GetCollectionDocumentKey(c.cid, did)

	{
		var oneshot, exists bool
		if txn == nil {
			oneshot = true
			txn = c.kvdb.NewTransaction(true)
			defer txn.Discard()
		}

		exists, err = txn.Exists(docKey)
		if err != nil {
			return
		}

		if !exists && !upsert {
			return
		}

		err = txn.Set(docKey, data, nil)
		if err != nil {
			return
		}

		if oneshot {
			err = txn.Commit()
			if err != nil {
				return
			}
		}

		updated = true
		isNew = !exists

		return
	}

}

// UpsertOne for upsert an existing document in collection
func (c *Collection) UpsertOne(did int64, doc bson.M, txn kvrpc.ProviderTxn) (isNew bool, err error) {
	_, isNew, err = c.updateOne(did, doc, true, txn)
	return
}

// DeleteOne for delete a document from collection
func (c *Collection) DeleteOne(did int64, txn kvrpc.ProviderTxn) (err error) {

	// prologue start
	err = c.db.checkState()
	if err != nil {
		return
	}
	err = c.db.closer.Add(1)
	if err != nil {
		return
	}
	defer c.db.closer.Done()
	// prologue end

	docKey := GetCollectionDocumentKey(c.cid, did)

	{
		var oneshot bool
		if txn == nil {
			oneshot = true
			txn = c.kvdb.NewTransaction(true)
			defer txn.Discard()
		}
		err = txn.Delete(docKey)
		if err != nil {
			return
		}
		if oneshot {
			err = txn.Commit()
			if err != nil {
				return
			}
		}

		return
	}

}

// GetOne for get a document by document id
func (c *Collection) GetOne(did int64, txn kvrpc.ProviderTxn) (data bson.M, err error) {
	// prologue start
	err = c.db.checkState()
	if err != nil {
		return
	}
	err = c.db.closer.Add(1)
	if err != nil {
		return
	}
	defer c.db.closer.Done()
	// prologue end

	docKey := GetCollectionDocumentKey(c.cid, did)
	if txn == nil {
		txn = c.kvdb.NewTransaction(false)
		defer txn.Discard()
	}
	v, _, err := txn.Get(docKey)
	if err == provider.ErrKeyNotFound {
		err = ErrDocNotFound
		return
	}
	if err != nil {
		return
	}

	err = bson.Unmarshal(v, &data)
	return
}

// GetMany for get many documents by document id list
func (c *Collection) GetMany(dids []int64, txn kvrpc.ProviderTxn) (datas []bson.M, err error) {
	// prologue start
	err = c.db.checkState()
	if err != nil {
		return
	}
	err = c.db.closer.Add(1)
	if err != nil {
		return
	}
	defer c.db.closer.Done()
	// prologue end

	if txn == nil {
		txn = c.kvdb.NewTransaction(false)
		defer txn.Discard()
	}

	var v []byte
	for _, did := range dids {
		docKey := GetCollectionDocumentKey(c.cid, did)
		v, _, err = txn.Get(docKey)
		if err == provider.ErrKeyNotFound {
			err = ErrDocNotFound
			return
		}
		if err != nil {
			return
		}
		var data bson.M
		err = bson.Unmarshal(v, &data)
		if err != nil {
			return
		}

		datas = append(datas, data)
	}
	return
}

func (c *Collection) close() {
	err := c.documentSequence.ReleaseRemaining()
	if err != nil {
		logger.Instance().Error("documentSequence.ReleaseRemaining", zap.Error(err))
	}
}

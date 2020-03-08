package meta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"errors"

	"github.com/zhiqiangxu/mondis"
	"github.com/zhiqiangxu/mondis/document"
	"github.com/zhiqiangxu/mondis/document/model"
	"github.com/zhiqiangxu/mondis/kv"
	"github.com/zhiqiangxu/mondis/structure"
)

// Meta is for handling meta information in a transaction.
type Meta struct {
	txn        *structure.TxStructure
	jobListKey JobListKeyType
}

var (
	globalIDMutex sync.Mutex
)

var (
	metaPrefixBytes        = []byte(document.BasePrefix + "m")
	dbsKey                 = []byte("dbs")
	nextGlobalIDKey        = []byte("nextGlobalID")
	schemaVersionKey       = []byte("schemaVersion")
	dbPrefix               = []byte("db")
	collectionPrefix       = []byte("collection")
	collectionAutoIDPrefix = []byte("collectionAutoID")
)

var (
	// ErrDBNotExists used by Meta
	ErrDBNotExists = errors.New("db not exists")
	// ErrDBExists used by Meta
	ErrDBExists = errors.New("db exists")
	// ErrCollectionExists used by Meta
	ErrCollectionExists = errors.New("collection exists")
	// ErrCollectionNotExists used by Meta
	ErrCollectionNotExists = errors.New("collection not exists")
)

// DDL job structure
//	DDLJobList: list jobs
//	DDLJobHistory: hash
//	DDLJobReorg: hash

var (
	mDDLJobListKey    = []byte("DDLJobList")
	mDDLJobAddIdxList = []byte("DDLJobAddIdxList")
	mDDLJobHistoryKey = []byte("DDLJobHistory")
	mDDLJobReorgKey   = []byte("DDLJobReorg")
)

// JobListKeyType is a key type of the DDL job queue.
type JobListKeyType []byte

var (
	// DefaultJobListKey keeps all actions of DDL jobs except "add index".
	DefaultJobListKey JobListKeyType = mDDLJobListKey
	// AddIndexJobListKey only keeps the action of adding index.
	AddIndexJobListKey JobListKeyType = mDDLJobAddIdxList
)

// NewMeta creates a Meta in transaction txn.
func NewMeta(txn mondis.ProviderTxn, jobListKeys ...JobListKeyType) *Meta {
	t := structure.New(txn, metaPrefixBytes)
	listKey := DefaultJobListKey
	if len(jobListKeys) != 0 {
		listKey = jobListKeys[0]
	}
	return &Meta{
		txn:        t,
		jobListKey: listKey,
	}
}

// GenGlobalID generates next id globally.
func (m *Meta) GenGlobalID() (int64, error) {
	globalIDMutex.Lock()
	defer globalIDMutex.Unlock()

	return m.txn.Inc(nextGlobalIDKey, 1)
}

// GenGlobalIDs generates the next n global IDs.
func (m *Meta) GenGlobalIDs(n int) ([]int64, error) {
	globalIDMutex.Lock()
	defer globalIDMutex.Unlock()

	newID, err := m.txn.Inc(nextGlobalIDKey, int64(n))
	if err != nil {
		return nil, err
	}
	origID := newID - int64(n)
	ids := make([]int64, 0, n)
	for i := origID + 1; i <= newID; i++ {
		ids = append(ids, i)
	}
	return ids, nil
}

// GetGlobalID gets current global id.
func (m *Meta) GetGlobalID() (int64, error) {
	return m.txn.GetInt64(nextGlobalIDKey)
}

func (m *Meta) dbKey(dbID int64) []byte {
	return []byte(fmt.Sprintf("%s:%d", dbPrefix, dbID))
}

func (m *Meta) collectionKey(collectionID int64) []byte {
	return []byte(fmt.Sprintf("%s:%d", collectionPrefix, collectionID))
}

func (m *Meta) collectionAutoIncrementIDKey(collectionID int64) []byte {
	return []byte(fmt.Sprintf("%s:%d", collectionAutoIDPrefix, collectionID))
}

func (m *Meta) checkDBExists(dbKey []byte) (err error) {
	_, err = m.txn.HGet(dbsKey, dbKey)
	if err == kv.ErrKeyNotFound {
		err = ErrDBNotExists
	}
	return
}

func (m *Meta) checkDBNotExists(dbKey []byte) (err error) {
	_, err = m.txn.HGet(dbsKey, dbKey)
	if err == kv.ErrKeyNotFound {
		err = nil
	} else if err == nil {
		err = ErrDBExists
	}
	return
}

func (m *Meta) checkCollectionExists(dbKey []byte, collectionKey []byte) (err error) {
	_, err = m.txn.HGet(dbKey, collectionKey)
	if err == kv.ErrKeyNotFound {
		err = ErrCollectionNotExists
	}
	return
}

func (m *Meta) checkCollectionNotExists(dbKey []byte, collectionKey []byte) (err error) {
	_, err = m.txn.HGet(dbKey, collectionKey)
	if err == kv.ErrKeyNotFound {
		err = nil
	} else if err == nil {
		err = ErrCollectionExists
	}
	return
}

// GenAutoCollectionID adds step to the auto ID of the collection and returns the sum.
func (m *Meta) GenAutoCollectionID(dbID, collectionID, step int64) (id int64, err error) {
	// Check if DB exists.
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}
	// Check if collection exists.
	collectionKey := m.collectionKey(collectionID)
	if err = m.checkCollectionExists(dbKey, collectionKey); err != nil {
		return
	}

	return m.txn.HInc(dbKey, m.collectionAutoIncrementIDKey(collectionID), step)
}

// GetAutoCollectionID gets current auto id with collection id.
func (m *Meta) GetAutoCollectionID(dbID, collectionID int64) (int64, error) {
	return m.txn.HGetInt64(m.dbKey(dbID), m.collectionAutoIncrementIDKey(collectionID))
}

// GetSchemaVersion gets current global schema version.
func (m *Meta) GetSchemaVersion() (int64, error) {
	return m.txn.GetInt64(schemaVersionKey)
}

// GenSchemaVersion generates next schema version.
func (m *Meta) GenSchemaVersion() (int64, error) {
	return m.txn.Inc(schemaVersionKey, 1)
}

// CreateDatabase creates a database with db info.
func (m *Meta) CreateDatabase(dbInfo *model.DBInfo) (err error) {
	dbKey := m.dbKey(dbInfo.ID)

	if err = m.checkDBNotExists(dbKey); err != nil {
		return
	}

	data, err := json.Marshal(dbInfo)
	if err != nil {
		return
	}

	return m.txn.HSet(dbsKey, dbKey, data)
}

// UpdateDatabase updates a database with db info.
func (m *Meta) UpdateDatabase(dbInfo *model.DBInfo) (err error) {
	dbKey := m.dbKey(dbInfo.ID)

	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	data, err := json.Marshal(dbInfo)
	if err != nil {
		return
	}

	return m.txn.HSet(dbsKey, dbKey, data)
}

// CreateCollection creates a collection with CollectoinInfo in database.
func (m *Meta) CreateCollection(dbID int64, collectionInfo *model.CollectionInfo) (err error) {
	// Check if db exists.
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	// Check if collection exists.
	collectionKey := m.collectionKey(collectionInfo.ID)
	if err = m.checkCollectionNotExists(dbKey, collectionKey); err != nil {
		return
	}

	data, err := json.Marshal(collectionInfo)
	if err != nil {
		return
	}

	return m.txn.HSet(dbKey, collectionKey, data)
}

// DropDatabase drops whole database.
func (m *Meta) DropDatabase(dbID int64) (err error) {
	// Check if db exists.
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	if err = m.txn.HClear(dbKey); err != nil {
		return
	}

	if err = m.txn.HDel(dbsKey, dbKey); err != nil {
		return
	}

	return
}

// DropCollection drops collection in database.
// If delAutoID is true, it will delete the auto_increment id key-value of the collection.
func (m *Meta) DropCollection(dbID int64, collectionID int64, delAutoID bool) (err error) {
	// Check if db exists.
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	// Check if collection exists.
	collectionKey := m.collectionKey(collectionID)
	if err = m.checkCollectionExists(dbKey, collectionKey); err != nil {
		return
	}

	if err = m.txn.HDel(dbKey, collectionKey); err != nil {
		return
	}
	if delAutoID {
		if err = m.txn.HDel(dbKey, m.collectionAutoIncrementIDKey(collectionID)); err != nil {
			return
		}
	}
	return
}

// UpdateCollection updates the collection with collection info.
func (m *Meta) UpdateCollection(dbID int64, collectionInfo *model.CollectionInfo) (err error) {
	// Check if db exists.
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	// Check if collection exists.
	collectionKey := m.collectionKey(collectionInfo.ID)
	if err = m.checkCollectionExists(dbKey, collectionKey); err != nil {
		return
	}

	data, err := json.Marshal(collectionInfo)
	if err != nil {
		return
	}

	err = m.txn.HSet(dbKey, collectionKey, data)
	return
}

// ListCollections shows all collections in database.
func (m *Meta) ListCollections(dbID int64) (collections []*model.CollectionInfo, err error) {
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	res, err := m.txn.HGetAll(dbKey)
	if err != nil {
		return
	}

	collections = make([]*model.CollectionInfo, 0, len(res)/2)
	for _, r := range res {
		// only handle collection meta
		if !bytes.HasPrefix(r.Field, collectionPrefix) {
			continue
		}

		tbInfo := &model.CollectionInfo{}
		err = json.Unmarshal(r.Value, tbInfo)
		if err != nil {
			return
		}

		collections = append(collections, tbInfo)
	}

	return
}

// ListDatabases shows all databases.
func (m *Meta) ListDatabases() (dbs []*model.DBInfo, err error) {
	res, err := m.txn.HGetAll(dbsKey)
	if err != nil {
		return
	}

	dbs = make([]*model.DBInfo, 0, len(res))
	for _, r := range res {
		dbInfo := &model.DBInfo{}
		err = json.Unmarshal(r.Value, dbInfo)
		if err != nil {
			return
		}
		dbs = append(dbs, dbInfo)
	}
	return
}

// GetDatabase gets the database value with ID.
func (m *Meta) GetDatabase(dbID int64) (dbInfo *model.DBInfo, err error) {
	dbKey := m.dbKey(dbID)
	value, err := m.txn.HGet(dbsKey, dbKey)
	if err == kv.ErrKeyNotFound {
		err = ErrDBNotExists
	}
	if err != nil {
		return
	}

	dbInfo = &model.DBInfo{}
	err = json.Unmarshal(value, dbInfo)
	return
}

// GetCollection gets the collection value in database with collectionID.
func (m *Meta) GetCollection(dbID int64, collectionID int64) (collectionnfo *model.CollectionInfo, err error) {
	// Check if db exists.
	dbKey := m.dbKey(dbID)
	if err = m.checkDBExists(dbKey); err != nil {
		return
	}

	tableKey := m.collectionKey(collectionID)
	value, err := m.txn.HGet(dbKey, tableKey)
	if err == kv.ErrKeyNotFound {
		err = ErrCollectionNotExists
	}
	if err != nil {
		return
	}

	collectionnfo = &model.CollectionInfo{}
	err = json.Unmarshal(value, collectionnfo)
	return
}

package test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/zhiqiangxu/kvrpc"
	"github.com/zhiqiangxu/kvrpc/client"
	"github.com/zhiqiangxu/kvrpc/document"
	"github.com/zhiqiangxu/kvrpc/provider"
	"github.com/zhiqiangxu/kvrpc/server"
	"go.mongodb.org/mongo-driver/bson"
)

const (
	addr    = "localhost:8099"
	dataDir = "/tmp/kvrpc"
)

func TestBadger(t *testing.T) {
	// server side
	{
		// use badger provider
		kvdb := provider.NewBadger()
		s := server.New(addr, kvdb, server.Option{}, kvrpc.KVOption{Dir: dataDir})
		go s.Start()

		time.Sleep(time.Millisecond * 500)

		defer s.Stop()
	}

	// client side
	{
		c := client.New(addr, client.Option{})
		nonExistingKey := []byte("nonExistingKey")

		{
			// test Exists
			exists, err := c.Exists(nonExistingKey)
			if err != nil || exists {
				t.Fatal("Exists nonExistingKey1")
			}
			// test Set
			key1 := []byte("key1")
			value1 := []byte("value1")
			err = c.Set(key1, value1, nil)
			if err != nil {
				t.Fatal("Set key1")
			}

			// test Get
			v, _, err := c.Get(key1)
			if err != nil || !bytes.Equal(v, value1) {
				t.Fatal("Get key1")
			}

			// test Delete
			err = c.Delete(key1)
			if err != nil {
				t.Fatal("Delete key1", err)
			}

			// test Get when key not exists
			_, _, err = c.Get(key1)
			if err != provider.ErrKeyNotFound {
				t.Fatal("Get key1")
			}
		}

		{
			// test Update transaction
			key2 := []byte("key2")
			value2 := []byte("value2")
			err := c.Update(func(txn kvrpc.Txn) error {
				err := txn.Set(key2, value2, nil)
				if err != nil {
					t.Fatal("Update.Set key2")
				}

				v, _, err := txn.Get(key2)
				if err != nil || !bytes.Equal(v, value2) {
					t.Fatal("Update.Get key2")
				}

				err = txn.Delete(key2)
				if err != nil {
					t.Fatal("Update.Delete key2")
				}

				_, _, err = txn.Get(key2)
				if err != provider.ErrKeyNotFound {
					t.Fatal("Update.Get key2")
				}

				// test Exists
				exists, err := txn.Exists(nonExistingKey)
				if err != nil || exists {
					t.Fatal("Exists nonExistingKey2")
				}
				return nil
			})
			if err != nil {
				t.Fatal("Update", err)
			}
		}

		{
			// test Read transaction
			key3 := []byte("key3")
			value3 := []byte("value3")
			err := c.Set(key3, value3, nil)
			if err != nil {
				t.Fatal("Set key3", err)
			}
			err = c.View(func(txn kvrpc.Txn) error {
				v, _, err := txn.Get(key3)
				if err != nil || !bytes.Equal(v, value3) {
					t.Fatal("View Get key3", err)
				}

				// test Exists
				exists, err := txn.Exists(nonExistingKey)
				if err != nil || exists {
					t.Fatal("Exists nonExistingKey3")
				}
				return nil
			})
			if err != nil {
				t.Fatal("View", err)
			}
			err = c.Delete(key3)
			if err != nil {
				t.Fatal("Delete key3", err)
			}
		}

		{
			// test Scan
			prefix := "unique_prefix"
			n := 10
			for i := 0; i < n; i++ {
				err := c.Set([]byte(fmt.Sprintf("%s:%d", prefix, i)), []byte{(byte(i))}, nil)
				if err != nil {
					t.Fatal("Set", err)
				}
			}

			var (
				entries []kvrpc.Entry
				err     error
			)
			scanOption := kvrpc.ScanOption{Limit: n - 1, ProviderScanOption: kvrpc.ProviderScanOption{Prefix: []byte(prefix)}}
			for j := 0; j < 2; j++ {
				switch j {
				case 0:
					entries, err = c.Scan(scanOption)
				case 1:
					err = c.Update(func(txn kvrpc.Txn) error {
						entries, err = txn.Scan(scanOption)
						return err
					})
				case 2:
					err = c.View(func(txn kvrpc.Txn) error {
						entries, err = txn.Scan(scanOption)
						return err
					})
				}

				if err != nil {
					t.Fatal("Scan", err)
				}
				if len(entries) != (n - 1) {
					t.Fatal("entry count not expected")
				}

				// scan result should be from low to high
				for i, entry := range entries {
					if !bytes.Equal(entry.Key, []byte(fmt.Sprintf("%s:%d", prefix, i))) {
						t.Fatal("entry not expected", i)
					}
				}
			}

		}

	}

}

func TestDocument(t *testing.T) {
	kvdb := provider.NewBadger()
	err := kvdb.Open(kvrpc.KVOption{Dir: dataDir})
	if err != nil {
		t.Fatal("kvdb.Open", err)
	}

	db := document.NewDB(kvdb)

	c, err := db.Collection("c")
	if err != nil {
		t.Fatal("db.Collection", err)
	}

	key := "key"
	did, err := c.InsertOne(bson.M{key: "value"}, nil)
	if err != nil {
		t.Fatal("c.InsertOne", err)
	}

	data, err := c.GetOne(did, nil)
	if err != nil {
		t.Fatal("c.GetOne", err)
	}
	if data[key] != "value" {
		t.Fatal("data[key] != \"value\"")
	}

	updated, err := c.UpdateOne(did, bson.M{key: "value2"}, nil)
	if err != nil || !updated {
		t.Fatal("c.InsertOne", err, updated)
	}

	data, err = c.GetOne(did, nil)
	if err != nil {
		t.Fatal("c.GetOne", err)
	}
	if data[key] != "value2" {
		t.Fatal("data[key] != \"value2\"")
	}

	err = c.DeleteOne(did, nil)
	if err != nil {
		t.Fatal("c.DeleteOne", err)
	}

	_, err = c.GetOne(did, nil)
	if err != document.ErrDocNotFound {
		t.Fatal("err != document.ErrDocNotFound", err)
	}
	db.Close()

}

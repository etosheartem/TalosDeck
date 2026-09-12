package operations

import (
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Check the database before touching running etcd. A matching transport hash
// alone says nothing about whether the bytes contain a recoverable database.
func verifySnapshotStructure(file string) (result error) {
	defer func() {
		if recover() != nil {
			result = errors.New("snapshot database structure is corrupt")
		}
	}()
	db, err := bolt.Open(file, 0600, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	if err != nil {
		return errors.New("snapshot is not a readable etcd database")
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		// Drain the checker even after an error, otherwise its goroutine can block.
		invalid := false
		for checkErr := range tx.Check() {
			if checkErr != nil {
				invalid = true
			}
		}
		if invalid {
			return errors.New("snapshot database page validation failed")
		}
		bucket := tx.Bucket([]byte("key"))
		if bucket == nil {
			return errors.New("snapshot lacks the etcd MVCC key bucket")
		}
		return bucket.ForEach(func(key, value []byte) error {
			if (len(key) != 17 && len(key) != 18) || key[8] != '_' || value == nil {
				return errors.New("snapshot contains an invalid etcd revision record")
			}
			return nil
		})
	})
	if err != nil {
		return errors.New("snapshot database structure is invalid")
	}
	return nil
}

package pool

import (
	"encoding"
	"geo-observers-blockchain/core/common/errors"
	"geo-observers-blockchain/core/common/types/hash"
	"geo-observers-blockchain/core/common/types/transactions"
	"geo-observers-blockchain/core/settings"
	"time"
)

type instance interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
	TxID() *transactions.TxID
}

type instances struct {
	At []instance
}

type Record struct {
	Instance instance

	//Approves collected from external observers.
	Approves [settings.KObserversMaxCount]bool

	// Time of last attempt to send this Record to the external observers.
	LastSyncAttempt time.Time
}

func (r *Record) IsMajorityApprovesCollected() bool {
	var (
		positiveVotesPresent = 0
		negativeVotesPresent = 0
	)

	for _, vote := range r.Approves {
		if vote == true {
			positiveVotesPresent++
			if positiveVotesPresent >= settings.ObserversConsensusCount {
				return true
			}

		} else {
			negativeVotesPresent++
			if negativeVotesPresent >= settings.ObserversMaxCount-settings.ObserversConsensusCount {
				return false
			}
		}
	}

	return false
}

type Pool struct {
	index map[hash.SHA256Container]*Record
}

func NewPool() *Pool {
	return &Pool{
		index: make(map[hash.SHA256Container]*Record),
	}
}

func (pool *Pool) Add(instance instance) (record *Record, err error) {
	data, err := instance.MarshalBinary()
	if err != nil {
		return
	}

	key := hash.NewSHA256Container(data)
	_, err = pool.ByHash(&key)
	if err != errors.NotFound {
		// Exactly the same item is already present in the pool.
		// It must not be replaced by the new value, to prevent votes dropping.
		err = errors.Collision
		return

	} else {
		err = nil
	}

	record = &Record{
		Instance: instance,
	}

	pool.index[key] = record
	return
}

func (pool *Pool) Remove(hash *hash.SHA256Container) {
	delete(pool.index, *hash)
}

func (pool *Pool) ByHash(hash *hash.SHA256Container) (record *Record, err error) {
	record, isPresent := pool.index[*hash]
	if !isPresent {
		return nil, errors.NotFound
	}

	return
}

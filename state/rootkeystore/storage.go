// Copyright 2018 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// Package rootkeystore provides an implementation of bakery.RootKeyStore
// that uses MongoDB as a persistent store.
package rootkeystore

import (
	"encoding/json"
	"time"

	"github.com/juju/errgo"
	"github.com/juju/errors"
	"github.com/juju/juju/mongo"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/dbrootkeystore"
)

// rootKeyDoc is the type stored for a root key in the MongoDB collection.
type rootKeyDoc struct {
	// Id holds the id of the root key.
	Id []byte `bson:"_id"`
	// Created holds the time that the root key was created.
	Created time.Time
	// Expires holds the time that the root key expires.
	Expires time.Time
	// RootKey holds the root key secret itself.
	RootKey []byte
}

func (key rootKeyDoc) dbRootKey() dbrootkeystore.RootKey {
	return dbrootkeystore.RootKey{
		Id:      key.Id,
		Created: key.Created,
		Expires: key.Expires,
		RootKey: key.RootKey,
	}
}

const (
	// CacheSize holds the approximate maximum number of keys that
	// will be held in memory as cache.
	CacheSize = 10

	// RootKeyLifetime holds the length of time that keys will
	// remain valid. This is more than any of the macaroon lifetimes
	// used in the rest of the code.
	RootKeyLifetime = 30 * 24 * time.Hour
)

// Config contains configuration for creating bakery storage with New.
type Config struct {
	// GetCollection returns a mongo.Collection and a function that
	// will close any associated resources.
	GetCollection func() (collection mongo.Collection, closer func())
}

// Policy holds a store policy for root keys.
type Policy = dbrootkeystore.Policy

// RootKeys represents a cache of macaroon root keys.
type RootKeys struct {
	getCollection func() (collection mongo.Collection, closer func())
	keys          *dbrootkeystore.RootKeys
}

// NewRootKeys returns a root-keys cache that
// is limited in size to approximately the given size.
//
// The NewStore method returns a store implementation
// that uses a specific mongo collection and store
// policy.
func NewRootKeys(cfg Config) *RootKeys {
	return &RootKeys{
		getCollection: cfg.GetCollection,
		keys:          dbrootkeystore.NewRootKeys(CacheSize, nil),
	}
}

// NewStore returns a RootKeyStore implementation that
// uses the underlying MongoDB store.
// The closer function should be called after the store has been
// used.
func (s *RootKeys) NewStore() (store bakery.RootKeyStore, closer func()) {
	c, closer := s.getCollection()
	return s.keys.NewStore(backing{c}, dbrootkeystore.Policy{
		ExpiryDuration: RootKeyLifetime,
	}), closer
}

var indexes = []mgo.Index{{
	Key: []string{"-created"},
}, {
	Key:         []string{"expires"},
	ExpireAfter: time.Second,
}}

// MongoIndexes returns the indexes to apply to the MongoDB collection.
func MongoIndexes() []mgo.Index {
	// Note: this second-guesses the underlying document format
	// used by bakery's mgostorage package.
	// TODO change things so that we can use EnsureIndex instead.
	return []mgo.Index{{
		Key: []string{"-created"},
	}, {
		Key:         []string{"expires"},
		ExpireAfter: time.Second,
	}}
}

// backing implements dbrootkeystore.Backing by using a
// mongo collection for storage.
type backing struct {
	coll mongo.Collection
}

// GetKey implements dbrootkeystore.Backing.GetKey.
func (b backing) GetKey(id []byte) (dbrootkeystore.RootKey, error) {
	var key rootKeyDoc
	err := b.coll.FindId(id).One(&key)
	if err != nil {
		if err == mgo.ErrNotFound {
			return b.legacyGetKey(id)
		}
		return dbrootkeystore.RootKey{}, errgo.Notef(err, "cannot get key from database")
	}
	// TODO migrate the key from the old format to the new format?
	return key.dbRootKey(), nil
}

// legacyStorageDoc holds the pre-bakery-v2 format of the root key store.
type legacyStorageDoc struct {
	Location string    `bson:"_id"`
	Item     string    `bson:"item"`
	ExpireAt time.Time `bson:"expire-at,omitempty"`
}

type legacyRootKey struct {
	RootKey []byte
}

// getLegacyFromMongo gets a value from the old version of the
// root key document which used a string key rather than a []byte
// key.
func (b backing) legacyGetKey(id []byte) (dbrootkeystore.RootKey, error) {
	var doc legacyStorageDoc
	err := b.coll.FindId(string(id)).One(&doc)
	if err != nil {
		if err == mgo.ErrNotFound {
			return dbrootkeystore.RootKey{}, bakery.ErrNotFound
		}
		return dbrootkeystore.RootKey{}, errors.Annotatef(err, "cannot get item for location %q", id)
	}
	var rootKey legacyRootKey
	err = json.Unmarshal([]byte(doc.Item), &rootKey)
	if err != nil {
		return dbrootkeystore.RootKey{}, errors.Annotate(err, "cannot unmarshal legacy rootkey")
	}
	// Note that we don't set the Created field, because we don't know
	// when the root key was created. This shouldn't matter because
	// the Created field is only used when finding root keys to be returned
	// for new macaroons, and we don't want to use legacy root keys for that.
	return dbrootkeystore.RootKey{
		Id:      id,
		RootKey: rootKey.RootKey,
		Expires: doc.ExpireAt,
	}, nil
}

func (b backing) FindLatestKey(createdAfter, expiresAfter, expiresBefore time.Time) (dbrootkeystore.RootKey, error) {
	var key rootKeyDoc
	err := b.coll.Find(bson.D{{
		"created", bson.D{{"$gte", createdAfter}},
	}, {
		"expires", bson.D{
			{"$gte", expiresAfter},
			{"$lte", expiresBefore},
		},
	}}).Sort("-created").One(&key)
	if err != nil && err != mgo.ErrNotFound {
		return dbrootkeystore.RootKey{}, errgo.Notef(err, "cannot query existing keys")
	}
	return key.dbRootKey(), nil
}

func (b backing) InsertKey(key dbrootkeystore.RootKey) error {
	doc := rootKeyDoc{
		Id:      key.Id,
		RootKey: key.RootKey,
		Created: key.Created,
		Expires: key.Expires,
	}
	if err := b.coll.Writeable().Insert(doc); err != nil {
		return errgo.Notef(err, "mongo insert failed")
	}
	return nil
}

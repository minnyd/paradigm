package storage

import (
	"fmt"
	"os"
	"strconv"

	"github.com/dgraph-io/badger"
	"github.com/paradigm-network/paradigm/errors"
	"github.com/paradigm-network/paradigm/types"
	"github.com/rs/zerolog"
	"github.com/paradigm-network/paradigm/common/log"
)

var (
	participantPrefix = "participant"
	rootSuffix        = "root"
	roundPrefix       = "round"
	topoPrefix        = "topo"
	blockPrefix       = "block"
)

type BadgerStore struct {
	participants map[string]int
	inmemStore   *InmemStore
	db           *badger.DB
	path         string
	logger       *zerolog.Logger
}

//NewBadgerStore creates a brand new Store with a new database
func NewBadgerStore(participants map[string]int, cacheSize int, path string) (*BadgerStore, error) {
	inmemStore := NewInmemStore(participants, cacheSize)
	opts := badger.DefaultOptions
	opts.Dir = path
	opts.ValueDir = path
	opts.SyncWrites = false
	handle, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	store := &BadgerStore{
		participants: participants,
		inmemStore:   inmemStore,
		db:           handle,
		path:         path,
		logger:       log.GetLogger("badger"),
	}

	if err := store.dbSetParticipants(participants); err != nil {
		return nil, err
	}
	store.logger.Info().Interface("rootsMap", inmemStore.roots).Msg("NewBadgerStore:dbSetRoots")
	if err := store.dbSetRoots(inmemStore.roots); err != nil {
		return nil, err
	}
	return store, nil
}

//LoadBadgerStore creates a Store from an existing database
func LoadBadgerStore(cacheSize int, path string) (*BadgerStore, error) {

	if _, err := os.Stat(path); err != nil {
		return nil, err
	}

	opts := badger.DefaultOptions
	opts.Dir = path
	opts.ValueDir = path
	opts.SyncWrites = false
	handle, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	store := &BadgerStore{
		db:     handle,
		path:   path,
		logger: log.GetLogger("badger"),
	}

	participants, err := store.dbGetParticipants()
	if err != nil {
		return nil, err
	}

	inmemStore := NewInmemStore(participants, cacheSize)

	//read roots from db and put them in InmemStore
	roots := make(map[string]types.Root)
	for p := range participants {
		root, err := store.dbGetRoot(p)
		if err != nil {
			return nil, err
		}
		roots[p] = root
	}

	if err := inmemStore.Reset(roots); err != nil {
		return nil, err
	}

	store.participants = participants
	store.inmemStore = inmemStore

	return store, nil
}

//==============================================================================
//Keys

func topologicalEventKey(index int) []byte {
	return []byte(fmt.Sprintf("%s_%09d", topoPrefix, index))
}

func participantKey(participant string) []byte {
	return []byte(fmt.Sprintf("%s_%s", participantPrefix, participant))
}

func participantEventKey(participant string, index int) []byte {
	return []byte(fmt.Sprintf("%s__event_%09d", participant, index))
}

func participantRootKey(participant string) []byte {
	return []byte(fmt.Sprintf("%s_%s", participant, rootSuffix))
}

func roundKey(index int) []byte {
	return []byte(fmt.Sprintf("%s_%09d", roundPrefix, index))
}

func blockKey(index int) []byte {
	return []byte(fmt.Sprintf("%s_%09d", blockPrefix, index))
}

//==============================================================================
//Implement the Store interface

func (s *BadgerStore) CacheSize() int {
	return s.inmemStore.CacheSize()
}

func (s *BadgerStore) Participants() (map[string]int, error) {
	return s.participants, nil
}

func (s *BadgerStore) GetComet(key string) (comet types.Comet, err error) {
	//try to get it from cache
	comet, err = s.inmemStore.GetComet(key)
	//if not in cache, try to get it from db
	if err != nil {
		comet, err = s.dbGetEvent(key)
	}
	return comet, mapError(err, key)
}

func (s *BadgerStore) SetComet(comet types.Comet) error {
	//try to add it to the cache
	if err := s.inmemStore.SetComet(comet); err != nil {
		return err
	}
	//try to add it to the db
	return s.dbSetEvents([]types.Comet{comet})
}

func (s *BadgerStore) ParticipantEvents(participant string, skip int) ([]string, error) {
	res, err := s.inmemStore.ParticipantEvents(participant, skip)
	if err != nil {
		res, err = s.dbParticipantEvents(participant, skip)
	}
	return res, err
}

func (s *BadgerStore) ParticipantEvent(participant string, index int) (string, error) {
	result, err := s.inmemStore.ParticipantEvent(participant, index)
	if err != nil {
		result, err = s.dbParticipantEvent(participant, index)
	}
	return result, mapError(err, string(participantEventKey(participant, index)))
}

func (s *BadgerStore) LastEventFrom(participant string) (last string, isRoot bool, err error) {
	return s.inmemStore.LastEventFrom(participant)
}

func (s *BadgerStore) KnownEvents() map[int]int {
	known := make(map[int]int)
	for p, pid := range s.participants {
		index := -1
		last, isRoot, err := s.LastEventFrom(p)
		s.logger.Info().Str("participant", p).Bool("isRoot", isRoot).Str("lastKet", last).Msg("KnownEvents:LastEventFrom")
		if err == nil {
			if isRoot {
				root, err := s.GetRoot(p)
				s.logger.Info().Str("participant", p).Bool("isRoot", isRoot).Int("rootIndex", root.Index).Msg("KnownEvents:GetRoot")
				if err != nil {
					last = root.X
					index = root.Index
				}
			} else {
				lastEvent, err := s.GetComet(last)
				s.logger.Info().Str("participant", p).Bool("isRoot", isRoot).Int("eventIndex", lastEvent.Index()).Msg("KnownEvents:GetComet")
				if err == nil {
					index = lastEvent.Index()
				}
			}

		}
		known[pid] = index
	}
	return known
}

func (s *BadgerStore) ConsensusEvents() []string {
	return s.inmemStore.ConsensusEvents()
}

func (s *BadgerStore) ConsensusEventsCount() int {
	return s.inmemStore.ConsensusEventsCount()
}

func (s *BadgerStore) AddConsensusEvent(key string) error {
	return s.inmemStore.AddConsensusEvent(key)
}

func (s *BadgerStore) GetRound(r int) (types.RoundInfo, error) {
	res, err := s.inmemStore.GetRound(r)
	if err != nil {
		res, err = s.dbGetRound(r)
	}
	return res, mapError(err, string(roundKey(r)))
}

func (s *BadgerStore) SetRound(r int, round types.RoundInfo) error {
	if err := s.inmemStore.SetRound(r, round); err != nil {
		return err
	}
	return s.dbSetRound(r, round)
}

func (s *BadgerStore) LastRound() int {
	return s.inmemStore.LastRound()
}

func (s *BadgerStore) RoundWitnesses(r int) []string {
	round, err := s.GetRound(r)
	if err != nil {
		return []string{}
	}
	return round.Witnesses()
}

func (s *BadgerStore) RoundEvents(r int) int {
	round, err := s.GetRound(r)
	if err != nil {
		return 0
	}
	return len(round.Events)
}

func (s *BadgerStore) GetRoot(participant string) (types.Root, error) {
	root, err := s.inmemStore.GetRoot(participant)
	if err != nil {
		root, err = s.dbGetRoot(participant)
	}
	return root, mapError(err, string(participantRootKey(participant)))
}

func (s *BadgerStore) GetBlock(rr int) (types.Block, error) {
	res, err := s.inmemStore.GetBlock(rr)
	if err != nil {
		res, err = s.dbGetBlock(rr)
	}
	return res, mapError(err, string(blockKey(rr)))
}

func (s *BadgerStore) SetBlock(block types.Block) error {
	if err := s.inmemStore.SetBlock(block); err != nil {
		return err
	}
	return s.dbSetBlock(block)
}

func (s *BadgerStore) Reset(roots map[string]types.Root) error {
	return s.inmemStore.Reset(roots)
}

func (s *BadgerStore) Close() error {
	if err := s.inmemStore.Close(); err != nil {
		return err
	}
	return s.db.Close()
}

//++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++
//DB Methods

func (s *BadgerStore) dbGetEvent(key string) (types.Comet, error) {
	var eventBytes []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		eventBytes, err = item.Value()
		return err
	})

	if err != nil {
		return types.Comet{}, err
	}

	comet := new(types.Comet)
	if err := comet.Unmarshal(eventBytes); err != nil {
		return types.Comet{}, err
	}

	return *comet, nil
}

func (s *BadgerStore) dbSetEvents(comets []types.Comet) error {
	tx := s.db.NewTransaction(true)
	defer tx.Discard()
	for _, comet := range comets {
		cometHex := comet.Hex()
		val, err := comet.Marshal()
		if err != nil {
			return err
		}
		//check if it already exists
		new := false
		_, err = tx.Get([]byte(cometHex))
		if err != nil && isDBKeyNotFound(err) {
			new = true
		}
		//insert [event hash] => [event bytes]
		if err := tx.Set([]byte(cometHex), val); err != nil {
			return err
		}

		if new {
			//insert [topo_index] => [event hash]
			topoKey := topologicalEventKey(comet.TopologicalIndex)
			if err := tx.Set(topoKey, []byte(cometHex)); err != nil {
				return err
			}
			//insert [participant_index] => [event hash]
			peKey := participantEventKey(comet.Creator(), comet.Index())
			if err := tx.Set(peKey, []byte(cometHex)); err != nil {
				return err
			}
		}
	}
	return tx.Commit(nil)
}

func (s *BadgerStore) DbTopologicalEvents() ([]types.Comet, error) {
	var res []types.Comet
	t := 0
	err := s.db.View(func(txn *badger.Txn) error {
		key := topologicalEventKey(t)
		item, errr := txn.Get(key)
		for errr == nil {
			v, errrr := item.Value()
			if errrr != nil {
				break
			}

			evKey := string(v)
			eventItem, err := txn.Get([]byte(evKey))
			if err != nil {
				return err
			}
			eventBytes, err := eventItem.Value()
			if err != nil {
				return err
			}

			comet := new(types.Comet)
			if err := comet.Unmarshal(eventBytes); err != nil {
				return err
			}
			res = append(res, *comet)

			t++
			key = topologicalEventKey(t)
			item, errr = txn.Get(key)
		}

		if !isDBKeyNotFound(errr) {
			return errr
		}

		return nil
	})

	return res, err
}

func (s *BadgerStore) dbParticipantEvents(participant string, skip int) ([]string, error) {
	res := []string{}
	err := s.db.View(func(txn *badger.Txn) error {
		i := skip + 1
		key := participantEventKey(participant, i)
		item, errr := txn.Get(key)
		for errr == nil {
			v, errrr := item.Value()
			if errrr != nil {
				break
			}
			res = append(res, string(v))

			i++
			key = participantEventKey(participant, i)
			item, errr = txn.Get(key)
		}

		if !isDBKeyNotFound(errr) {
			return errr
		}

		return nil
	})
	return res, err
}

func (s *BadgerStore) dbParticipantEvent(participant string, index int) (string, error) {
	data := []byte{}
	key := participantEventKey(participant, index)
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		data, err = item.Value()
		return err
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *BadgerStore) dbSetRoots(roots map[string]types.Root) error {
	tx := s.db.NewTransaction(true)
	defer tx.Discard()
	for participant, root := range roots {
		val, err := root.Marshal()
		if err != nil {
			return err
		}
		key := participantRootKey(participant)
		s.logger.Info().Str("participant", participant).Str("key", string(key)).Msg("dbSetRoots")
		//insert [participant_root] => [root bytes]
		if err := tx.Set(key, val); err != nil {
			return err
		}
	}
	return tx.Commit(nil)
}

func (s *BadgerStore) dbGetRoot(participant string) (types.Root, error) {
	var rootBytes []byte
	key := participantRootKey(participant)
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		rootBytes, err = item.Value()
		return err
	})

	if err != nil {
		return types.Root{}, err
	}

	root := new(types.Root)
	if err := root.Unmarshal(rootBytes); err != nil {
		return types.Root{}, err
	}

	return *root, nil
}

func (s *BadgerStore) dbGetRound(index int) (types.RoundInfo, error) {
	var roundBytes []byte
	key := roundKey(index)
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		roundBytes, err = item.Value()
		return err
	})

	if err != nil {
		return *types.NewRoundInfo(), err
	}

	roundInfo := new(types.RoundInfo)
	if err := roundInfo.Unmarshal(roundBytes); err != nil {
		return *types.NewRoundInfo(), err
	}

	return *roundInfo, nil
}

func (s *BadgerStore) dbSetRound(index int, round types.RoundInfo) error {
	tx := s.db.NewTransaction(true)
	defer tx.Discard()

	key := roundKey(index)
	val, err := round.Marshal()
	if err != nil {
		return err
	}

	//insert [round_index] => [round bytes]
	if err := tx.Set(key, val); err != nil {
		return err
	}

	return tx.Commit(nil)
}

func (s *BadgerStore) dbGetParticipants() (map[string]int, error) {
	res := make(map[string]int)
	err := s.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		prefix := []byte(participantPrefix)
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			k := string(item.Key())
			v, err := item.Value()
			if err != nil {
				return err
			}
			//key is of the form participant_0x.......
			pubKey := k[len(participantPrefix)+1:]
			id, err := strconv.Atoi(string(v))
			if err != nil {
				return err
			}
			res[pubKey] = id
		}
		return nil
	})
	return res, err
}

func (s *BadgerStore) dbSetParticipants(participants map[string]int) error {
	tx := s.db.NewTransaction(true)
	defer tx.Discard()
	for participant, id := range participants {
		key := participantKey(participant)
		val := []byte(strconv.Itoa(id))
		//insert [participant_participant] => [id]
		if err := tx.Set(key, val); err != nil {
			return err
		}
	}
	return tx.Commit(nil)
}

func (s *BadgerStore) dbGetBlock(index int) (types.Block, error) {
	var blockBytes []byte
	key := blockKey(index)
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		blockBytes, err = item.Value()
		return err
	})

	if err != nil {
		return types.Block{}, err
	}

	block := new(types.Block)
	if err := block.Unmarshal(blockBytes); err != nil {
		return types.Block{}, err
	}

	return *block, nil
}

func (s *BadgerStore) dbSetBlock(block types.Block) error {
	tx := s.db.NewTransaction(true)
	defer tx.Discard()

	key := blockKey(block.Index())
	val, err := block.Marshal()
	if err != nil {
		return err
	}

	//insert [index] => [block bytes]
	if err := tx.Set(key, val); err != nil {
		return err
	}

	return tx.Commit(nil)
}

func (s *BadgerStore) Get(key []byte) (value []byte, err error) {
	err = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		value, err = item.Value()
		return err
	})

	if err != nil {
		return nil, err
	}
	return
}
func (s *BadgerStore) Has(key []byte) (has bool, err error) {
	err = s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		has = item.EstimatedSize() != 0
		return err
	})

	if err != nil {
		return false, err
	}
	return
}
func (s *BadgerStore) Put(key, value []byte) error {
	tx := s.db.NewTransaction(true)
	defer tx.Discard()

	if err := tx.Set(key, value); err != nil {
		return err
	}
	return tx.Commit(nil)
}

//++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++

func isDBKeyNotFound(err error) bool {
	return err.Error() == badger.ErrKeyNotFound.Error()
}

func mapError(err error, key string) error {
	if err != nil {
		if isDBKeyNotFound(err) {
			return errors.NewStoreErr(errors.KeyNotFound, key)
		}
	}
	return err
}

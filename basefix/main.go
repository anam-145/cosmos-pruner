// basefix: diagnose / repair the cometbft blockstore base after offline pruning.
//
// Run from the cosmos-pruner repo root so go.mod deps resolve:
//
//	# NODE MUST BE STOPPED.
//	go run ./basefix --home /home/ubuntu/.celestia/snap-mainnet            # read-only diagnose
//	go run ./basefix --home /home/ubuntu/.celestia/snap-mainnet --set      # repair base
//
// It reads the stored BlockStoreState{Base,Height}, scans every surviving
// "H:<height>" block-meta key to find the TRUE lowest height (parsed as int,
// not lexically), and with --set rewrites Base to that height. Then /status
// LoadBaseMeta(base) finds a real meta -> earliest_block_* populates.
package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	cmtstore "github.com/cometbft/cometbft/api/cometbft/store/v1"
	db "github.com/cosmos/cosmos-db"
	"github.com/cosmos/gogoproto/proto"
)

var blockStoreKey = []byte("blockStore")

func main() {
	home := flag.String("home", "", "node home (contains data/)")
	backend := flag.String("backend", "goleveldb", "db backend: goleveldb|pebbledb|rocksdb|badgerdb")
	set := flag.Bool("set", false, "write the repaired base (omit = read-only)")
	flag.Parse()
	if *home == "" {
		log.Fatal("--home required")
	}

	dataDir := filepath.Join(*home, "data")
	bdb, err := db.NewDB("blockstore", db.BackendType(*backend), dataDir)
	if err != nil {
		log.Fatalf("open blockstore.db: %v", err)
	}
	defer bdb.Close()

	// current stored state
	raw, err := bdb.Get(blockStoreKey)
	if err != nil {
		log.Fatalf("get blockStore key: %v", err)
	}
	var bss cmtstore.BlockStoreState
	if len(raw) > 0 {
		if err := proto.Unmarshal(raw, &bss); err != nil {
			log.Fatalf("unmarshal BlockStoreState: %v", err)
		}
	}
	fmt.Printf("STORED   base=%d height=%d\n", bss.Base, bss.Height)

	// does meta at current base exist?
	if bss.Base > 0 {
		ok, _ := bdb.Has(fmt.Appendf(nil, "H:%d", bss.Base))
		fmt.Printf("meta H:%d exists=%v  (if false -> LoadBaseMeta nil -> /status earliest empty)\n", bss.Base, ok)
	}

	// true lowest surviving block-meta height
	lowest, found, err := lowestMetaHeight(bdb)
	if err != nil {
		log.Fatalf("scan H: keys: %v", err)
	}
	if !found {
		log.Fatal("no H: block-meta keys found - blockstore empty?")
	}
	fmt.Printf("LOWEST surviving block meta H:%d\n", lowest)

	if !*set {
		fmt.Println("read-only. re-run with --set to write base =", lowest)
		return
	}

	bss.Base = lowest
	out, err := proto.Marshal(&bss)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := bdb.SetSync(blockStoreKey, out); err != nil { // SetSync = flush to disk
		log.Fatalf("write blockStore key: %v", err)
	}

	// read back from the SAME handle to confirm it landed
	chk, _ := bdb.Get(blockStoreKey)
	var v cmtstore.BlockStoreState
	_ = proto.Unmarshal(chk, &v)
	fmt.Printf("WROTE    base=%d height=%d (read-back base=%d)\n", lowest, bss.Height, v.Base)
	fmt.Println("now start the node and: curl -s localhost:26657/status | jq .result.sync_info")
}

func lowestMetaHeight(bdb db.DB) (int64, bool, error) {
	start := []byte("H:")
	end := []byte("H;") // ':' + 1, covers all "H:" keys
	it, err := bdb.Iterator(start, end)
	if err != nil {
		return 0, false, err
	}
	defer it.Close()

	var min int64
	found := false
	for ; it.Valid(); it.Next() {
		hs := strings.TrimPrefix(string(it.Key()), "H:")
		h, err := strconv.ParseInt(hs, 10, 64) // numeric, not lexical
		if err != nil {
			continue
		}
		if !found || h < min {
			min, found = h, true
		}
	}
	return min, found, it.Error()
}

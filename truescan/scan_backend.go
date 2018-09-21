package truescan

import (
	"encoding/hex"
	"fmt"

	"github.com/truechain/truechain-engineering-code/core"
	"github.com/truechain/truechain-engineering-code/core/snailchain"
	"github.com/truechain/truechain-engineering-code/core/types"
	"github.com/truechain/truechain-engineering-code/event"
)

func bytesToHex(input []byte) string {
	return "0x" + hex.EncodeToString(input)
}

// TrueScan provides the ability to proactively roll out messages to Redis services.
type TrueScan struct {
	sub               Subscriber
	running           bool
	addTxCh           chan core.AddTxEvent
	addTxSub          event.Subscription
	removeTxCh        chan core.RemoveTxEvent
	removeTxSub       event.Subscription
	fastBlockCh       chan core.FastBlockEvent
	fastBlockSub      event.Subscription
	fruitsch          chan snailchain.NewFruitsEvent //fruit
	fruitsSub         event.Subscription             // for fruit pool
	snailChainHeadCh  chan snailchain.ChainHeadEvent
	snailChainHeadSub event.Subscription
	electionCh        chan core.ElectionEvent
	electionSub       event.Subscription
	stateChangeCh     chan core.StateChangeEvent
	stateChangeSub    event.Subscription
	redisClient       *RedisClient
	quit              chan struct{}
}

// New function return a TrueScan message processing client
func New(sub Subscriber, config *Config) *TrueScan {
	ts := &TrueScan{
		sub:              sub,
		running:          false,
		addTxCh:          make(chan core.AddTxEvent, addTxChanSize),
		removeTxCh:       make(chan core.RemoveTxEvent, removeTxChanSize),
		fastBlockCh:      make(chan core.FastBlockEvent, fastBlockChanSize),
		fruitsch:         make(chan snailchain.NewFruitsEvent, fruitChanSize),
		snailChainHeadCh: make(chan snailchain.ChainHeadEvent, snailChainHeadSize),
		electionCh:       make(chan core.ElectionEvent, electionChanSize),
		stateChangeCh:    make(chan core.StateChangeEvent, stateChangeChanSize),
		quit:             make(chan struct{}),
	}
	rc, err := NewRedisClient(config)
	if err != nil {
		return nil
	}
	ts.redisClient = rc
	return ts
}

// Start TrueScan message processing client
func (ts *TrueScan) Start() {
	ts.running = true
	ts.redisClient.Start()

	// broadcast transactions
	ts.addTxSub = ts.sub.SubscribeAddTxEvent(ts.addTxCh)
	go ts.txHandleLoop()

	ts.removeTxSub = ts.sub.SubscribeRemoveTxEvent(ts.removeTxCh)
	go ts.removeTxHandleLoop()

	// Subscribe events from blockchain
	ts.fastBlockSub = ts.sub.SubscribeFastBlock(ts.fastBlockCh)
	go ts.fastBlockHandleLoop()

	ts.fruitsSub = ts.sub.SubscribeNewFruitEvent(ts.fruitsch)
	go ts.fruitHandleLoop()

	ts.snailChainHeadSub = ts.sub.SubscribeSnailChainHeadEvent(ts.snailChainHeadCh)
	go ts.snailChainHandleLoop()

	ts.electionSub = ts.sub.SubscribeElectionEvent(ts.electionCh)
	go ts.electionHandleLoop()

	ts.stateChangeSub = ts.sub.SubscribeStateChangeEvent(ts.stateChangeCh)
	go ts.stateChangeHandleLoop()

	go ts.loop()
}

func (ts *TrueScan) txHandleLoop() error {
	for {
		select {
		case addTxEvent := <-ts.addTxCh:
			ts.handleTx(addTxEvent.Tx)
		case <-ts.addTxSub.Err():
			return errResp("tx terminated")
		}
	}
}

func (ts *TrueScan) handleTx(tx *types.Transaction) {
	from, err := types.NewEIP155Signer(tx.ChainId()).Sender(tx)
	if err != nil {
		return
	}
	var toHex string
	if to := tx.To(); to == nil {
		toHex = ""
	} else {
		toHex = to.String()
	}
	tm := &TransactionMsg{
		Nonce:    tx.Nonce(),
		Hash:     tx.Hash().String(),
		From:     from.String(),
		To:       toHex,
		Value:    bytesToHex(tx.Value().Bytes()),
		Gas:      tx.Gas(),
		GasPrice: bytesToHex(tx.GasPrice().Bytes()),
		Input:    bytesToHex(tx.Data()),
	}
	ts.redisClient.PendingTransaction(tm)
}

func (ts *TrueScan) removeTxHandleLoop() error {
	for {
		select {
		case removeTxEvent := <-ts.removeTxCh:
			ts.handleRemoveTx(removeTxEvent)
		case <-ts.removeTxSub.Err():
			return errResp("removeTx terminated")
		}
	}
}

func (ts *TrueScan) handleRemoveTx(rte core.RemoveTxEvent) {
	rtm := &RemoveTxMsg{
		Hash: rte.Hash.String(),
	}
	ts.redisClient.RemoveTransaction(rtm)
}

func (ts *TrueScan) electionHandleLoop() error {
	for {
		select {
		case electionEvent := <-ts.electionCh:
			if electionEvent.Option == types.CommitteeStart {
				ts.handleElection(&electionEvent)
			}
		case <-ts.electionSub.Err():
			return errResp("election terminated")
		}
	}
}
func (ts *TrueScan) handleElection(ee *core.ElectionEvent) {
	bfn := ee.BeginFastNumber
	if bfn == nil {
		return
	}
	members := ee.CommitteeMembers
	mas := make([]string, len(members))
	for i, member := range members {
		mas[i] = member.Coinbase.String()
	}
	cvm := &ChangeViewMsg{
		ViewNumber:      ee.CommitteeID.Uint64(),
		Members:         mas,
		BeginFastNumber: bfn.Uint64(),
	}
	ts.redisClient.ChangeView(cvm)
}

func (ts *TrueScan) stateChangeHandleLoop() error {
	for {
		select {
		case stateChangeEvent := <-ts.stateChangeCh:
			ts.handleStateChange(stateChangeEvent)
		case <-ts.stateChangeSub.Err():
			return errResp("state terminated")
		}
	}
}

func (ts *TrueScan) handleStateChange(bsd core.StateChangeEvent) {
	balances := make([]*Account, len(bsd.Balances))
	rewards := make([]*Account, len(bsd.Rewards))
	for i, b := range bsd.Balances {
		balances[i] = &Account{
			Address: b.Address.String(),
			Value:   bytesToHex(b.Balance.Bytes()),
		}
	}
	for i, r := range bsd.Rewards {
		rewards[i] = &Account{
			Address: r.Address.String(),
			Value:   bytesToHex(r.Balance.Bytes()),
		}
	}
	scm := &StateChangeMsg{
		Balances: balances,
		Rewards:  rewards,
	}
	ts.redisClient.StateChange(scm)
}

func (ts *TrueScan) snailChainHandleLoop() error {
	for {
		select {
		case snailChainEvent := <-ts.snailChainHeadCh:
			ts.handleSnailChain(snailChainEvent.Block)
		case <-ts.fruitsSub.Err():
			return errResp("fruit terminated")
		}
	}
}

func (ts *TrueScan) handleSnailChain(block *types.SnailBlock) {
	sbm := &SnailBlockHeaderMsg{
		Number:     block.NumberU64(),
		Hash:       block.Hash().String(),
		ParentHash: block.ParentHash().String(),
		Nonce:      block.Nonce(),
		Miner:      block.Coinbase().String(),
		Difficulty: block.Difficulty().Uint64(),
		ExtraData:  bytesToHex(block.Extra()),
		Size:       block.Size().Int(),
		Timestamp:  block.Time().Uint64(),
	}
	fruits := block.Fruits()
	fms := make([]*FruitHeaderMsg, len(fruits))
	for i, fruit := range fruits {
		fm := &FruitHeaderMsg{
			Number:     fruit.FastNumber().Uint64(),
			Hash:       fruit.FruitsHash().String(),
			Nonce:      fruit.Nonce(),
			Miner:      fruit.Coinbase().String(),
			Difficulty: fruit.Difficulty().Uint64(),
		}
		fms[i] = fm
	}
	sbm.StartFruitNumber = fms[0].Number
	sbm.EndFruitNumber = fms[len(fms)-1].Number
	sbm.Fruits = fms
	ts.redisClient.NewSnailBlockHeader(sbm)
}

func (ts *TrueScan) fruitHandleLoop() error {
	for {
		select {
		case fruitsEvent := <-ts.fruitsch:
			ts.handleFruits(fruitsEvent.Fruits)
		case <-ts.fruitsSub.Err():
			return errResp("fruit terminated")
		}
	}
}

func (ts *TrueScan) handleFruits(fruits []*types.SnailBlock) {
	fmt.Println("-----------------")
}

func (ts *TrueScan) fastBlockHandleLoop() error {
	for {
		select {
		case fastBlockEvent := <-ts.fastBlockCh:
			ts.handleFastChain(fastBlockEvent)
		case <-ts.fastBlockSub.Err():
			return errResp("fast block terminated")
		}
	}
}

func (ts *TrueScan) handleFastChain(fbe core.FastBlockEvent) {
	block := fbe.Block
	receipts := fbe.Receipts
	fbm := &FastBlockHeaderMsg{
		Number:     block.NumberU64(),
		Hash:       block.Hash().String(),
		ParentHash: block.ParentHash().String(),
		ExtraData:  bytesToHex(block.Extra()),
		Size:       block.Size().Int(),
		GasLimit:   block.GasLimit(),
		GasUsed:    block.GasUsed(),
		Timestamp:  block.Time().Uint64(),
	}
	txs := block.Transactions()
	ftms := make([]*FullTransactionMsg, len(txs))
	for i, tx := range txs {
		hash := tx.Hash()
		r := receipts[hash]
		if r == nil {
			continue
		}
		from, err := types.NewEIP155Signer(tx.ChainId()).Sender(tx)
		if err != nil {
			continue
		}
		var toHex, contractHex string
		if to := tx.To(); to == nil {
			toHex = ""
			contractHex = r.ContractAddress.String()
		} else {
			toHex = to.String()
			contractHex = ""
		}
		ftm := &FullTransactionMsg{
			Nonce:             tx.Nonce(),
			Hash:              hash.String(),
			From:              from.String(),
			To:                toHex,
			Value:             bytesToHex(tx.Value().Bytes()),
			Gas:               tx.Gas(),
			GasPrice:          bytesToHex(tx.GasPrice().Bytes()),
			Input:             bytesToHex(tx.Data()),
			PostState:         bytesToHex(r.PostState),
			Status:            r.Status == 1,
			CumulativeGasUsed: r.CumulativeGasUsed,
			Bloom:             bytesToHex(r.Bloom.Bytes()),
			Logs:              r.Logs,
			ContractAddress:   contractHex,
			GasUsed:           r.GasUsed,
		}
		ftms[i] = ftm
	}
	fbm.Txs = ftms
	ts.redisClient.NewFastBlockHeader(fbm)
}

func (ts *TrueScan) loop() error {
	for {
		select {
		case <-ts.quit:
			// TrueScan terminating, abort all operations
			return errTerminated
		}
	}
}

// Stop TrueScan message processing client
func (ts *TrueScan) Stop() {
	if !ts.running {
		return
	}
	ts.addTxSub.Unsubscribe()
	ts.removeTxSub.Unsubscribe()
	ts.fastBlockSub.Unsubscribe()
	ts.fruitsSub.Unsubscribe()
	ts.snailChainHeadSub.Unsubscribe()
	ts.electionSub.Unsubscribe()
	ts.stateChangeSub.Unsubscribe()
	close(ts.quit)
}
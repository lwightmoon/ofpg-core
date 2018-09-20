package p2p

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ofgp/common/defines"
	"github.com/ofgp/ofgp-core/log"
	"github.com/ofgp/ofgp-core/message"

	ew "swap/ethwatcher"

	"github.com/golang/protobuf/proto"
	"github.com/ofgp/ofgp-core/business"
	"github.com/ofgp/ofgp-core/node"
	pb "github.com/ofgp/ofgp-core/proto"
)

var p2pLogger = log.New("DEBUG", "node")

// P2P 点对点交换
type P2P struct {
	ch      chan node.BusinessEvent
	node    *node.BraftNode
	handler business.IHandler
}

const businessName = "p2p"

// NewP2P create p2p
func NewP2P(braftNode *node.BraftNode, path string) *P2P {
	p2p := &P2P{
		node: braftNode,
	}
	ldb, _ := node.OpenDbOrDie(path, "p2pdb")

	db := newP2PDB(ldb)
	//向node订阅业务相关事件
	ch := braftNode.SubScribe(businessName)
	p2p.ch = ch

	//创建交易匹配索引
	p2pInfos := db.getAllP2PInfos()
	index := newTxIndex()

	index.AddInfos(p2pInfos)
	service := newService(braftNode)
	wh := &watchedHandler{
		db:                 db,
		node:               braftNode,
		index:              index,
		checkMatchInterval: time.Duration(1) * time.Second,
		service:            service,
	}
	// check匹配超时
	wh.runCheckMatchTimeout()

	sh := newSignedHandler(db, service, 5, 1)
	sh.runCheck()
	confirmH := newConfirmHandler(db, 5, service, 1)
	confirmH.runCheck()
	commitH := &commitHandler{}
	wh.SetSuccessor(sh)
	sh.SetSuccessor(confirmH)
	confirmH.SetSuccessor(commitH)
	p2p.handler = wh

	return p2p
}

func (p2p *P2P) Run() {
	go p2p.processEvent()
}
func (p2p *P2P) processEvent() {
	for event := range p2p.ch {
		p2p.handler.HandleEvent(event)
	}
}

type watchedHandler struct {
	service *service
	sync.Mutex
	db    *p2pdb
	index *txIndex
	node  *node.BraftNode
	business.Handler
	checkMatchInterval time.Duration
}

func createTx(node *node.BraftNode, op int, info *P2PInfo) interface{} {
	return nil
}

// getP2PInfo p2p交易数据
func getP2PInfo(event *pb.WatchedEvent) *P2PInfo {
	msg := &p2pMsg{}
	msg.Decode(event.GetData())
	p2pMsg := msg.toPBMsg()
	info := &P2PInfo{
		Event: event,
		Msg:   p2pMsg,
		Time:  time.Now().Unix(),
	}
	return info
}

// setWaitConfirm 设置 txID 和 matchedTxID 的双向对应关系
func setWaitConfirm(db *p2pdb, op, chain uint32, txID string) {
	waitConfirmMsg := &WaitConfirmMsg{
		Opration: op,
		Chain:    chain,
		Info:     nil,
	}
	db.setWaitConfirm(txID, waitConfirmMsg)
}

// isConfirmed 判断交易是否已被确认
func isConfirmed(db *p2pdb, txID string) bool {
	waitConfirm := db.getWaitConfirm(txID)
	return waitConfirm != nil && waitConfirm.Info != nil
}

// isMatching 是否在匹配中
func isMatching(db *p2pdb, txID string) bool {
	return db.ExistMatched(txID)
}

// checkP2PInfo check点对点交易是否合法
func (wh *watchedHandler) checkP2PInfo(info *P2PInfo) bool {
	txID := info.GetScTxID()
	if isMatching(wh.db, txID) {
		p2pLogger.Warn("tx is matching or has already been matched", "scTxID", txID)
		return false
	}
	if info.IsExpired() {
		p2pLogger.Warn("tx has already been expired", "scTxID", txID)
		return false
	}
	return true
}

//checkMatchTimeout 交易是否匹配超时
func (wh *watchedHandler) checkMatchTimeout() {
	wh.Lock()
	defer wh.Unlock()
	infos := wh.db.getAllP2PInfos()
	for _, info := range infos {
		// match 超时
		scTxID := info.GetScTxID()
		if !isMatching(wh.db, info.GetScTxID()) && !wh.node.IsDone(scTxID) && info.IsExpired() {
			//创建并发送回退交易
			p2pLogger.Debug("match timeout", "scTxID", info.GetScTxID())
			wh.db.setMatchedOne(info.GetScTxID(), "")
			newTx, err := wh.service.createTx(back, info)
			if newTx != nil && err == nil {
				waitToSign := newWaitToSign(info, newTx)
				wh.service.sendtoSign(waitToSign)
			}
			setWaitConfirm(wh.db, uint32(back), info.Event.GetTo(), info.GetScTxID())
		}
	}
}

func (wh *watchedHandler) runCheckMatchTimeout() {
	ticker := time.NewTicker(wh.checkMatchInterval).C
	go func() {
		for {
			<-ticker
			wh.checkMatchTimeout()
		}
	}()
}

func newWaitToSign(info *P2PInfo, newTx *pb.NewlyTx) *message.WaitSignMsg {
	var waitToSign *message.WaitSignMsg
	switch info.Msg.Chain {
	case message.Btc:
		fallthrough
	case message.Bch:
		recharge := &pb.BtcRecharge{
			Amount: info.Msg.Amount,
			Addr:   info.Msg.ReceiveAddr,
		}
		rechargeData, _ := proto.Marshal(recharge)
		waitToSign = &message.WaitSignMsg{
			Business: info.Event.Business,
			ID:       info.Event.GetTxID(),
			ScTxID:   info.Event.GetTxID(),
			Tx:       newTx,
			Recharge: rechargeData,
		}
	case message.Eth:
		recharge := &pb.EthRecharge{
			Addr:     info.Msg.ReceiveAddr,
			Amount:   info.Msg.Amount,
			TokenTo:  info.Msg.TokenId,
			Method:   ew.VOTE_METHOD_MATCHSWAP,
			Proposal: info.Event.GetTxID(),
		}
		rechargeData, _ := proto.Marshal(recharge)
		waitToSign = &message.WaitSignMsg{
			Business: info.Event.Business,
			ID:       info.Event.GetTxID(),
			ScTxID:   info.Event.GetTxID(),
			Tx:       newTx,
			Recharge: rechargeData,
		}

	}
	return waitToSign
}
func (wh *watchedHandler) HandleEvent(event node.BusinessEvent) {
	wh.Lock()
	defer wh.Unlock()
	if watchedEvent, ok := event.(*node.WatchedEvent); ok {
		txEvent := watchedEvent.GetData()
		if event == nil {
			p2pLogger.Error("data is nil", "business", watchedEvent.GetBusiness())
			return
		}
		info := getP2PInfo(txEvent)
		if !wh.checkP2PInfo(info) {
			p2pLogger.Debug("check p2pinfo fail", "scTxID", info.GetScTxID)
			return
		}

		wh.db.setP2PInfo(info)
		// p2pLogger.Debug("add coniditon", "chian", info.Event.From, "addr", info.Msg.SendAddr, "amount", info.Event.Amount)
		wh.index.Add(info)
		//要求匹配的条件
		chain, addr, amount := info.getExchangeInfo()
		// p2pLogger.Debug("search coniditon", "chian", chain, "addr", addr, "amount", amount)
		// 使用要求的数据匹配交易数据
		txIDs := wh.index.GetTxID(chain, addr, amount)
		p2pLogger.Debug("matchIDs", "txIDs", txIDs)
		if txIDs != nil {
			for _, txID := range txIDs {
				matchedInfo := wh.db.getP2PInfo(txID)
				if matchedInfo == nil {
					p2pLogger.Error("get p2pInfo nil", "business", event.GetBusiness(), "scTxID", txEvent.GetTxID())
					continue
				}
				if !wh.checkP2PInfo(matchedInfo) {
					p2pLogger.Debug("check matched p2pinfo fail", "scTxID", info.GetScTxID)
					continue
				}
				//保存已匹配的两笔交易的txid
				wh.db.setMatched(info.GetScTxID(), matchedInfo.GetScTxID())
				//删除索引 防止重复匹配
				wh.index.Del(info)

				infos := []*P2PInfo{info, matchedInfo}
				//创建交易发送签名
				for _, info := range infos {
					newTx, err := wh.service.createTx(confirmed, info)
					if newTx != nil && err == nil {
						signMsg := newWaitToSign(info, newTx)
						wh.service.sendtoSign(signMsg)
					}
					//设置等待确认
					setWaitConfirm(wh.db, uint32(confirmed), info.Event.GetTo(), info.GetScTxID())
				}

				break
			}
		} else {
			p2pLogger.Debug("handle watchedEvent not matched", "scTxID", txEvent.GetTxID())
		}

	} else if wh.Successor != nil {
		wh.Successor.HandleEvent(event)
	}

}

type signedHandler struct {
	business.Handler
	db          *p2pdb
	service     *service
	signFailTxs map[string]*WaitConfirmMsg //签名失败
	signTimeout int64
	sync.Mutex
	interval time.Duration
}

func newSignedHandler(db *p2pdb, service *service, signTimeout int64, interval time.Duration) *signedHandler {
	return &signedHandler{
		db:          db,
		service:     service,
		signFailTxs: make(map[string]*WaitConfirmMsg),
		signTimeout: signTimeout,
		interval:    interval,
	}
}

func (sh *signedHandler) addSignFailed(wait *WaitConfirmMsg) {
	sh.signFailTxs[wait.ScTxId] = wait
}
func (sh *signedHandler) isInSignFailed(scTxID string) bool {
	_, ok := sh.signFailTxs[scTxID]
	return ok
}
func (sh *signedHandler) isSignTimeout(wait *WaitConfirmMsg) bool {
	return time.Now().Unix()-wait.Time > sh.signTimeout
}

func (sh *signedHandler) delSignFailed(scTxID string) {
	delete(sh.signFailTxs, scTxID)
}

func (sh *signedHandler) retryFailed() {
	for _, waitConfirmTx := range sh.signFailTxs {
		scTxID := waitConfirmTx.ScTxId
		if sh.service.isSignFail(scTxID) { //本term失败
			continue
		} else {
			sh.Lock()
			if sh.db.existSendedInfo(scTxID) {
				sh.delSignFailed(scTxID)
				p2pLogger.Warn("already signed", "scTxID", scTxID)
				sh.Unlock()
				continue
			}
			if sh.service.isDone(scTxID) {
				p2pLogger.Warn("already finished", "scTxID", scTxID)
				sh.delSignFailed(scTxID)
				sh.db.clear(scTxID)
				sh.Unlock()
				continue
			}
			p2pInfo := sh.db.getP2PInfo(scTxID)
			if p2pInfo == nil {
				p2pLogger.Error("check confirm timeout p2pInfo is nil", "scTxID", scTxID)
				sh.delSignFailed(scTxID)
				sh.db.clear(scTxID)
				sh.Unlock()
				continue
			}

			newTx, _ := sh.service.createTx(uint8(waitConfirmTx.Opration), p2pInfo)
			if newTx != nil {
				waitToSign := newWaitToSign(p2pInfo, newTx)
				sh.service.sendtoSign(waitToSign)
			}
			sh.delSignFailed(scTxID)
			waitConfirmTx.Time = time.Now().Unix()
			sh.db.setWaitConfirm(scTxID, waitConfirmTx)
			sh.Unlock()
		}
	}
}
func (sh *signedHandler) runCheck() {
	ticker := time.NewTicker(sh.interval * time.Second).C
	go func() {
		for {
			<-ticker
			sh.checkSignTimeout()
		}
	}()
}
func (sh *signedHandler) checkSignTimeout() {
	waitTxs := sh.db.getAllWaitConfirm()
	sh.retryFailed()
	for _, waitTx := range waitTxs {
		scTxID := waitTx.ScTxId
		if sh.isInSignFailed(scTxID) {
			continue
		}
		//sign timeout
		sh.Lock()
		if sh.isSignTimeout(waitTx) && !sh.db.existSendedInfo(scTxID) {
			p2pLogger.Debug("sign timeout", "scTxID", scTxID)
			sh.service.markSignFail(scTxID)
			sh.addSignFailed(waitTx) //下一个term重试
			// checker.service.accuse() todo
		}
		sh.Unlock()
	}
}
func (sh *signedHandler) HandleEvent(event node.BusinessEvent) {
	if signedEvent, ok := event.(*node.SignedEvent); ok {
		p2pLogger.Info("handle signed")
		signedData := signedEvent.GetData()
		if signedData == nil {
			p2pLogger.Error("signed data is nil")
			return
		}
		txID := signedData.TxID
		sh.Lock()
		if !sh.service.isSignFail(txID) && !sh.isInSignFailed(txID) && !sh.db.existSendedInfo(txID) && !sh.service.isDone(txID) {
			p2pLogger.Debug("receive signedData", "scTxID", signedData.ID)
			//发送交易
			err := sh.service.sendTx(signedData)
			if err != nil {
				p2pLogger.Error("send tx err", "err", err, "scTxID", signedData.ID, "business", signedEvent.Business)
			}
			sh.db.setSendedInfo(&SendedInfo{
				TxId:           signedData.TxID,
				SignTerm:       signedData.Term,
				Chain:          signedData.Chain,
				SignBeforeTxId: signedData.SignBeforeTxID,
			})
		} else {
			p2pLogger.Debug("already sended", "scTxID", txID)
		}
		sh.Unlock()
	} else if sh.Successor != nil {
		sh.Successor.HandleEvent(event)
	}
}

type confirmHandler struct {
	sync.Mutex
	db *p2pdb
	business.Handler
	confirmTolerance int64
	service          *service
	interval         time.Duration
}

func newConfirmHandler(db *p2pdb, confirmTolerance int64, service *service, interval time.Duration) *confirmHandler {
	return &confirmHandler{
		db:               db,
		confirmTolerance: confirmTolerance,
		service:          service,
		interval:         interval,
	}
}

// getPubTxFromInfo huoqu g
func getPubTxFromInfo(info *P2PInfo) *pb.PublicTx {
	if info == nil {
		return nil
	}
	event := info.GetEvent()
	chain := uint8(event.GetTo())
	var rechargeData []byte
	switch chain {
	case defines.CHAIN_CODE_BTC:
		fallthrough
	case defines.CHAIN_CODE_BCH:
		recharge := &pb.BtcRecharge{
			Addr:   info.GetMsg().GetReceiveAddr(),
			Amount: info.GetMsg().GetAmount(),
		}
		rechargeData, _ = proto.Marshal(recharge)
	case defines.CHAIN_CODE_ETH:
		recharge := &pb.EthRecharge{
			Addr:   info.GetMsg().GetReceiveAddr(),
			Amount: info.GetMsg().GetAmount(),
		}
		rechargeData, _ = proto.Marshal(recharge)
	}
	pubTx := &pb.PublicTx{
		Chain:    event.GetFrom(),
		TxID:     event.GetTxID(),
		Amount:   int64(event.GetAmount()),
		Data:     event.GetData(),
		Recharge: rechargeData,
	}
	return pubTx
}
func getVin(infos []*P2PInfo) []*pb.PublicTx {
	pubtxs := make([]*pb.PublicTx, 0)
	for _, info := range infos {
		pubtx := getPubTxFromInfo(info)
		if pubtx != nil {
			pubtxs = append(pubtxs, pubtx)
		}
	}
	return pubtxs
}

func getPubTxFromConfirmInfo(info *P2PConfirmInfo) *pb.PublicTx {
	if info == nil {
		return nil
	}
	event := info.GetEvent()
	pubTx := &pb.PublicTx{
		Chain:  event.GetFrom(),
		TxID:   event.GetTxID(),
		Amount: int64(event.GetAmount()),
		Data:   event.GetData(),
	}
	return pubTx
}

func getVout(infos []*P2PConfirmInfo) []*pb.PublicTx {

	pubtxs := make([]*pb.PublicTx, 0)
	for _, info := range infos {
		pubtx := getPubTxFromConfirmInfo(info)
		if pubtx != nil {
			pubtxs = append(pubtxs, pubtx)
		}
	}
	return pubtxs
}

// createDGWTx 创建网关交易
func createDGWTx(business string, infos []*P2PInfo, confirmInfos []*P2PConfirmInfo) *pb.Transaction {
	tx := &pb.Transaction{
		Business: business,
		Vin:      getVin(infos),
		Vout:     getVout(confirmInfos),
		Time:     time.Now().Unix(),
	}
	tx.UpdateId()
	return tx
}

// commitTx commit点对点交易
func (handler *confirmHandler) commitTx(business string, infos []*P2PInfo, confirmInfos []*P2PConfirmInfo) {
	p2pLogger.Debug("commit dgw tx", "business", business)
	dgwTx := createDGWTx(business, infos, confirmInfos)
	txJSON, _ := json.Marshal(dgwTx)
	p2pLogger.Debug("commit data", "data", string(txJSON))
	handler.service.commitTx(dgwTx)
}

func getP2PConfirmInfo(event *pb.WatchedEvent) *P2PConfirmInfo {
	msg := &p2pMsgConfirmed{}
	msg.Decode(event.GetData())
	pbMsg := msg.toPBMsg()
	info := &P2PConfirmInfo{
		Event: event,
		Msg:   pbMsg,
	}
	return info
}

func (handler *confirmHandler) getConfirmTimeout(chain uint32) int64 {
	switch chain {
	case message.Bch:
		fallthrough
	case message.Btc:
		return int64(node.BchConfirms)*60 + handler.confirmTolerance
	case message.Eth:
		return int64(node.BchConfirms)*15 + handler.confirmTolerance
	default:
		return handler.confirmTolerance
	}

}

func (handler *confirmHandler) isConfirmTimeout(sendedInfo *SendedInfo) bool {
	return (time.Now().Unix() - sendedInfo.Time) > handler.getConfirmTimeout(sendedInfo.Chain)
}

func (handler *confirmHandler) runCheck() {
	ticker := time.NewTicker(time.Second).C
	go func() {
		for {
			<-ticker
			handler.checkConfirmTimeout()
		}
	}()
}

// checkConfirmTimeout check链上确认超时
func (handler *confirmHandler) checkConfirmTimeout() {
	sendedInfos := handler.db.getAllSendedInfo()
	for _, sended := range sendedInfos {
		if handler.isConfirmTimeout(sended) {
			scTxID := sended.TxId
			p2pLogger.Debug("confirm timeout", "scTxID", scTxID)
			waitConfirm := handler.db.getWaitConfirm(scTxID)
			if waitConfirm != nil {
				p2pLogger.Error("check confirm timeout waitconfirm is nil", "scTxID", scTxID)
				continue
			}
			p2pInfo := handler.db.getP2PInfo(scTxID)
			if p2pInfo == nil {
				p2pLogger.Error("check confirm timeout p2pInfo is nil", "scTxID", scTxID)
				handler.db.clear(scTxID)
				continue
			}
			// check交易是否在链上存在 btc bch
			if sended.Chain == message.Bch || sended.Chain == message.Btc {
				if handler.service.isTxOnChain(sended.SignBeforeTxId, uint8(sended.Chain)) {
					p2pLogger.Info("check confirm already onchain", "scTxID", scTxID)
					continue
				}
			}

			newTx, err := handler.service.createTx(uint8(waitConfirm.Opration), p2pInfo)
			if err != nil {
				p2pLogger.Error("create tx err", "err", err, "scTxID", scTxID)
				continue
			}
			handler.service.clear(scTxID, sended.SignTerm)
			handler.db.delSendedInfo(scTxID)
			if newTx != nil {
				waitToSign := newWaitToSign(p2pInfo, newTx)
				handler.service.sendtoSign(waitToSign)
			}

			//update waitconfirm time
			waitConfirm.Time = time.Now().Unix()
			handler.db.setWaitConfirm(scTxID, waitConfirm)
			handler.service.accuseWithTerm(sended.SignTerm)
		}
	}
}
func (handler *confirmHandler) HandleEvent(event node.BusinessEvent) {
	if confirmedEvent, ok := event.(*node.ConfirmEvent); ok {
		txEvent := confirmedEvent.GetData()
		if event == nil {
			p2pLogger.Error("confirm data is nil")
			return
		}
		//交易确认info
		info := getP2PConfirmInfo(txEvent)
		p2pLogger.Info("handle confirm", "scTxID", info.Msg.Id)

		//之前的交易id
		oldTxID := txEvent.GetProposal()
		waitConfirm := handler.db.getWaitConfirm(oldTxID)
		if waitConfirm == nil {
			p2pLogger.Error("never matched tx", "scTxID", oldTxID)
			return
		}
		if waitConfirm.Opration == confirmed { //确认交易 需要等待发起和匹配交易确认

			waitConfirm.Info = info
			handler.db.setWaitConfirm(oldTxID, waitConfirm)

			//先前匹配的交易id
			oldMatchedTxID := handler.db.getMatched(oldTxID)
			oldWaitConfirm := handler.db.getWaitConfirm(oldMatchedTxID)
			if oldWaitConfirm.Info != nil { //与oldTxID匹配的交易已被confirm
				confirmInfos := []*P2PConfirmInfo{info, oldWaitConfirm.Info}
				oldTxIDs := []string{oldTxID, oldMatchedTxID}
				p2pLogger.Debug("get old info", "txIDs", oldTxIDs)
				p2pInfos := handler.db.getP2PInfos(oldTxIDs)
				handler.commitTx(event.GetBusiness(), p2pInfos, confirmInfos)
			} else {
				p2pLogger.Info("wait another tx confirm", "scTxID", oldTxID)
			}

		} else if waitConfirm.Opration == back { //回退交易 commit当前confirmInfo和对应的p2pInfo
			p2pLogger.Debug("hanle confirm back", "scTxID", info.Msg.Id)
			confirmInfos := []*P2PConfirmInfo{info}
			oldTxIDs := []string{oldTxID}
			p2pInfos := handler.db.getP2PInfos(oldTxIDs)
			handler.commitTx(event.GetBusiness(), p2pInfos, confirmInfos)
		} else {
			p2pLogger.Error("oprationtype wrong", "opration", waitConfirm.Opration)
		}

	} else if handler.Successor != nil {
		handler.Successor.HandleEvent(event)
	}
}

type commitHandler struct {
	business.Handler
	db *p2pdb
}

func (ch *commitHandler) HandleEvent(event node.BusinessEvent) {
	if val, ok := event.(*node.CommitedEvent); ok {
		commitedData := val.GetData()
		if commitedData == nil || commitedData.Tx == nil {
			p2pLogger.Error("commit data is nil")
			return
		}
		var matchedTxs []string
		for _, scTx := range commitedData.Tx.Vin {
			scTxID := scTx.TxID
			matchedTxs = append(matchedTxs, scTxID)
			ch.db.clear(scTxID)
		}
		p2pLogger.Info("commited txID", "matched", matchedTxs, "business", val.Business)
		// p2pLogger.Info("handle Commited", "innerTxID", commitedData.Tx.TxID)
	} else {
		p2pLogger.Error("could not handle event")
	}
}
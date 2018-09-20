package node

import (
	"github.com/ofgp/common/defines"
	"github.com/ofgp/ofgp-core/message"
	pb "github.com/ofgp/ofgp-core/proto"
)

//CreateTx create tx
func (node *BraftNode) CreateTx(req CreateReq) (*pb.NewlyTx, error) {
	tx, err := node.leader.createTx(req)
	return tx, err
}

//SignTx 发送到待签名队列
func (node *BraftNode) SignTx(msg *message.WaitSignMsg) {
	node.txStore.AddTxtoWaitSign(msg)
}

// SendTx sendTx
func (node *BraftNode) SendTx(req ISendReq) error {
	err := node.txInvoker.SendTx(req)
	return err
}

// Commit 创建网关tx
func (node *BraftNode) Commit(req *pb.Transaction) {
	node.txStore.CreateInnerTx(req)
}

// IsDone 判断网关是否处理完成
func (node *BraftNode) IsDone(scTxID string) bool {
	inMem := node.txStore.IsTxInMem(scTxID)
	indb := node.txStore.HasTxInDB(scTxID)
	return inMem || indb
}

// GetTxByHash TODO 根据签名前交易查询是否链上存在
func (node *BraftNode) GetTxByHash(txid string, chain uint8) defines.PushEvent {
	switch chain {
	case defines.CHAIN_CODE_BCH:
		return node.bchWatcher.GetTxByHash(txid)
	case defines.CHAIN_CODE_BTC:
		return node.btcWatcher.GetTxByHash(txid)
	case defines.CHAIN_CODE_ETH:
		event, _ := node.ethWatcher.GetEventByHash(txid)
		return event
	}
	return nil
}

// Clear 清理签名数据 业务方重试使用
func (node *BraftNode) Clear(scTxID string, term int64) {
	if node.txStore.IsTxInMem(scTxID) && node.txStore.HasTxInDB(scTxID) {
		return
	}
	node.blockStore.MarkFailedSignRecord(scTxID, term)
	node.signedResultCache.Delete(scTxID)
	node.blockStore.DeleteSignReq(scTxID)
	//删除等待队列
	node.txStore.DeleteWaitSign(scTxID)
	//删除已签名标记
	node.signedTxs.Delete(scTxID)
}

// AccuseWithTerm 业务发起accuse
func (node *BraftNode) AccuseWithTerm(term int64) {
	node.accuser.TriggerByBusiness(term)
}

// Accuse 使用当前term发起accuse
func (node *BraftNode) Accuse() {
	node.accuser.TriggerByBusiness(node.blockStore.GetNodeTerm())
}

// MarkFail 标记交易本term失败
func (node *BraftNode) MarkFail(scTxID string) {
	node.blockStore.MarkFailedSignRecord(scTxID, node.blockStore.GetNodeTerm())
}

// IsSignFailed 判断签名本term内是否失败
func (node *BraftNode) IsSignFailed(scTxID string) bool {
	return node.blockStore.IsSignFailed(scTxID, node.blockStore.GetNodeTerm())
}
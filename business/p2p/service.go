package p2p

import (
	"github.com/ofgp/ofgp-core/message"
	"github.com/ofgp/ofgp-core/node"
	pb "github.com/ofgp/ofgp-core/proto"
)

type service struct {
	node *node.BraftNode
}

func (s *service) createTx(op uint8, info *P2PInfo) (*pb.NewlyTx, error) {
	var addr []byte
	msg := info.GetMsg()
	event := info.GetEvent()
	switch op {
	case confirmed:
		addr = msg.ReceiveAddr
	case back:
		addr = msg.SendAddr
	default:
		p2pLogger.Error("op err")
		return nil, nil
	}
	var req node.CreateReq
	switch msg.Chain {
	case message.Bch:
		fallthrough
	case message.Btc:
		req = &node.BaseCreateReq{
			Chain:  msg.Chain,
			ID:     event.GetTxID(),
			Addr:   addr,
			Amount: msg.Amount,
		}
	case message.Eth:
		ethReq := &node.EthCreateReq{}
		ethReq.Chain = msg.Chain
		ethReq.ID = event.GetTxID()
		ethReq.Addr = addr
		ethReq.Amount = msg.Amount
		ethReq.TokenTo = msg.TokenId
		req = ethReq
	default:
		p2pLogger.Error("chain type err")
	}
	tx, err := s.node.CreateTx(req)
	return tx, err
}

func (s *service) sendtoSign(signReq *message.WaitSignMsg) {
	p2pLogger.Debug("send to sign")
}
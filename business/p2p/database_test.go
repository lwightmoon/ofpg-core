package p2p

import (
	"fmt"
	"time"

	"github.com/ofgp/ofgp-core/message"
	pb "github.com/ofgp/ofgp-core/proto"
)

func ExampleP2PInfo() {
	requireAddr := getBytes(20)
	p2pMsg := &p2pMsg{
		SendAddr:    getBytes(20),
		ReceiveAddr: getBytes(20),
		Chain:       1,
		TokenID:     1,
		Amount:      64,
		Fee:         1,
		ExpiredTime: uint32(time.Now().Unix()),
		RequireAddr: requireAddr,
	}
	msgUse := p2pMsg.toPBMsg()
	event := &pb.WatchedEvent{
		TxID:   "testTxID",
		Amount: 1,
		From:   message.Bch,
		To:     message.Eth,
		Data:   p2pMsg.Encode(),
	}
	p2pInfo := &P2PInfo{
		Event: event,
		Msg:   msgUse,
	}
	p2pDB.setP2PInfo(p2pInfo)
	info := p2pDB.getP2PInfo(event.TxID, message.Bch)
	fmt.Printf("get p2pInfo txID:%s\n", info.Event.TxID)
	// Output: get p2pInfo txID:testTxID
}

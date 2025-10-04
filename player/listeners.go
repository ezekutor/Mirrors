package player

import (
	"os"
	"sync/atomic"
	"time"
	"wirstaff.com/mirrors/player/appticket"
	"wirstaff.com/mirrors/protocol"
	"wirstaff.com/mirrors/protocol/protobuf"
	"wirstaff.com/mirrors/protocol/steamlang"
)

type LoggedOnEvent struct {
	Result steamlang.EResult
}

type AppOwnershipTicketResponse struct {
	Ticket *appticket.AppTicket
}

func (p *Player) HandlePacket(packet *protocol.Packet) {
	switch packet.EMsg {
	case steamlang.EMsg_ClientLogOnResponse:
		p.handleLogOnResponse(packet)
	case steamlang.EMsg_ClientGameConnectTokens:
		p.handleGameConnectTokens(packet)
	case steamlang.EMsg_ClientGetAppOwnershipTicketResponse:
		p.handleGetAppOwnershipTicketResponse(packet)
	case steamlang.EMsg_ClientAuthListAck:
		p.handleAuthListAck(packet)
	}
}

func (p *Player) handleLogOnResponse(packet *protocol.Packet) {
	body := new(protobuf.CMsgClientLogonResponse)
	msg := packet.ReadProtoMsg(body)

	if body.GetEresult() == 1 {
		p.client.SetSessionId(msg.Header.Proto.GetClientSessionid())
		p.client.SetSteamId(msg.Header.Proto.GetSteamid())

		p.connectTime = time.Unix(int64(body.GetRtime32ServerTime()), 0)
		atomic.StoreUint32(&p.publicIp, body.GetPublicIp().GetV4())

		builder := new(protobuf.CMsgClientRequestWebAPIAuthenticateUserNonce)

		p.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientRequestWebAPIAuthenticateUserNonce, builder))

	}

	p.client.Emit(&LoggedOnEvent{Result: steamlang.EResult(body.GetEresult())})
}

func (p *Player) handleGameConnectTokens(packet *protocol.Packet) {
	body := new(protobuf.CMsgClientGameConnectTokens)
	packet.ReadProtoMsg(body)
	p.gcTokensMutex.Lock()
	p.gcTokens = append(p.gcTokens, body.Tokens...)

	if len(p.gcTokens) > int(body.GetMaxTokensToKeep()) {
		p.gcTokens = p.gcTokens[1:len(p.gcTokens)]
	}

	p.gcTokensMutex.Unlock()
}

func (p *Player) handleGetAppOwnershipTicketResponse(packet *protocol.Packet) {
	body := new(protobuf.CMsgClientGetAppOwnershipTicketResponse)
	packet.ReadProtoMsg(body)

	if body.GetEresult() != uint32(steamlang.EResult_OK) {
		p.client.Emit(&AppOwnershipTicketResponse{Ticket: nil})
		return
	}

	os.WriteFile(p.generateAppTicketFileCacheName(body.GetAppId()), body.GetTicket(), 0o744)
	ticket, err := appticket.NewAppTicket(body.GetTicket())
	if err != nil {
		p.client.Emit(&AppOwnershipTicketResponse{Ticket: nil})
		return
	}

	p.client.Emit(&AppOwnershipTicketResponse{Ticket: ticket})
}

func (p *Player) handleAuthListAck(packet *protocol.Packet) {
	body := new(protobuf.CMsgClientAuthListAck)
	packet.ReadProtoMsg(body)

	p.messageSequenceMutex.Lock()
	p.messageSequence = body.MessageSequence
	p.messageSequenceMutex.Unlock()
}

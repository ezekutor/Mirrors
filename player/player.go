package player

import (
	"encoding/binary"
	"fmt"
	"google.golang.org/protobuf/proto"
	"hash/crc32"
	"io/ioutil"
	"os"
	"sync"
	"time"
	"wirstaff.com/mirrors/player/appticket"
	"wirstaff.com/mirrors/protocol"
	"wirstaff.com/mirrors/protocol/protobuf"
	"wirstaff.com/mirrors/protocol/steamlang"
	"wirstaff.com/mirrors/steam"
	"wirstaff.com/mirrors/steamid"
)

type Player struct {
	client *steam.Client

	connectTime     time.Time
	connectionCount int
	gcTokens        [][]byte
	gcTokensMutex   sync.Mutex
	publicIp        uint32
	tickets         []*protobuf.CMsgAuthTicket
	ticketsMutex    sync.Mutex

	messageSequence      *uint32
	messageSequenceMutex sync.Mutex

	authSeqMe      uint32
	authSeqMeMutex sync.Mutex
}

type Ticket struct {
	SteamId uint64
	Crc     uint32
	Ticket  []byte
}

func New() *Player {
	player := &Player{
		gcTokens: make([][]byte, 0),

		client: steam.New(),
	}

	player.client.RegisterPacketHandler(player)
	player.client.Connect()

	return player
}

func (p *Player) Events() <-chan interface{} {
	return p.client.Events()
}

func (p *Player) Logon(login string, password string) {
	builder := new(protobuf.CMsgClientLogon)

	builder.AccountName = proto.String(login)
	builder.Password = proto.String(password)
	builder.ProtocolVersion = proto.Uint32(steamlang.MsgClientLogon_CurrentProtocol)
	p.client.SetSteamId(uint64(steamid.NewIdAdv(0, 1, int32(steamlang.EUniverse_Public), int32(steamlang.EAccountType_Individual))))
	p.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientLogon, builder))
}

func (p *Player) GetSteamId() steamid.SteamId {
	return p.client.GetSteamId()
}

func (p *Player) pullGcToken() []byte {
	p.gcTokensMutex.Lock()
	defer p.gcTokensMutex.Unlock()

	if len(p.gcTokens) == 0 {
		return nil
	}

	token := p.gcTokens[0]
	p.gcTokens = p.gcTokens[1:len(p.gcTokens)]

	return token
}

func (p *Player) GetAppOwnershipTicket(appId uint32) {
	cachedTickedFileName := p.generateAppTicketFileCacheName(appId)

	appTicket, err := ioutil.ReadFile(cachedTickedFileName)
	if err == nil && len(appTicket) > 0 {
		parsedTicket, err := appticket.NewAppTicket(appTicket)

		if err == nil && parsedTicket.IsExpired(time.Now().Add(time.Minute)) == false {
			p.client.Emit(&AppOwnershipTicketResponse{Ticket: parsedTicket})
			return
		} else {
			os.Remove(cachedTickedFileName)
		}
	}

	p.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientGetAppOwnershipTicket, &protobuf.CMsgClientGetAppOwnershipTicket{
		AppId: proto.Uint32(appId),
	}))
}

func (p *Player) createAuthToken(gameConnectToken []byte) []byte {
	var buf1 [4]byte
	var buf2 [28]byte

	timestamp := uint32(time.Now().Sub(p.connectTime).Milliseconds())

	binary.LittleEndian.PutUint32(buf1[0:], uint32(len(gameConnectToken)))

	binary.LittleEndian.PutUint32(buf2[0:], 6*4)
	binary.LittleEndian.PutUint32(buf2[4:], 1)
	binary.LittleEndian.PutUint32(buf2[8:], 2)
	binary.BigEndian.PutUint32(buf2[12:], p.publicIp)
	binary.LittleEndian.PutUint32(buf2[16:], 0)
	binary.LittleEndian.PutUint32(buf2[20:], timestamp)
	binary.LittleEndian.PutUint32(buf2[24:], 1)

	result := make([]byte, 0)
	result = append(result, buf1[:]...)
	result = append(result, gameConnectToken...)
	result = append(result, buf2[:]...)

	return result
}

func (p *Player) AuthSessionTicket(ticket *appticket.AppTicket) (*Ticket, error) {
	var buf1 [4]byte
	var buf2 [32]byte
	var buf3 [4]byte

	gcToken := p.pullGcToken()

	if gcToken == nil {
		return nil, fmt.Errorf("empty gc token")
	}

	bufTicket := ticket.OriginalBuffer()

	p.connectionCount++

	timestamp := uint32(time.Now().Sub(p.connectTime).Milliseconds())

	binary.LittleEndian.PutUint32(buf1[:], uint32(len(gcToken)))

	binary.LittleEndian.PutUint32(buf2[0:], 24)
	binary.LittleEndian.PutUint32(buf2[4:], 1)
	binary.LittleEndian.PutUint32(buf2[8:], 2)
	binary.BigEndian.PutUint32(buf2[12:], p.publicIp)
	binary.LittleEndian.PutUint32(buf2[16:], 0)
	binary.LittleEndian.PutUint32(buf2[20:], timestamp)
	binary.LittleEndian.PutUint32(buf2[24:], uint32(p.connectionCount))
	binary.LittleEndian.PutUint32(buf2[28:], 1)

	result := make([]byte, 0)
	result = append(result, buf1[:]...)
	result = append(result, gcToken...)
	result = append(result, buf2[:]...)
	binary.LittleEndian.PutUint32(buf3[:], uint32(len(bufTicket)))
	result = append(result, buf3[:]...)
	result = append(result, bufTicket...)

	gameId := uint64(ticket.AppId)

	authToken := p.createAuthToken(gcToken)

	ticketCrc := crc32.ChecksumIEEE(authToken)

	authTicket := protobuf.CMsgAuthTicket{
		Gameid:    &gameId,
		Ticket:    authToken,
		TicketCrc: &ticketCrc,
	}

	p.ticketsMutex.Lock()
	p.tickets = append(p.tickets, &authTicket)
	p.ticketsMutex.Unlock()

	p.SendTickets()

	return &Ticket{
		SteamId: p.GetSteamId().ToUint64(),
		Crc:     ticketCrc,
		Ticket:  result,
	}, nil
}

func (p *Player) SendTickets() {
	p.ticketsMutex.Lock()
	p.NormalizeTickets()
	tokensLeft := uint32(len(p.gcTokens))
	builder := new(protobuf.CMsgClientAuthList)
	builder.TokensLeft = &tokensLeft

	p.authSeqMeMutex.Lock()
	builder.LastRequestSeq = proto.Uint32(p.authSeqMe)
	p.authSeqMe += 1
	builder.MessageSequence = proto.Uint32(p.authSeqMe)
	p.authSeqMeMutex.Unlock()

	p.messageSequenceMutex.Lock()
	builder.LastRequestSeqFromServer = p.messageSequence
	p.messageSequenceMutex.Unlock()

	builder.AppIds = []uint32{240}
	builder.Tickets = p.tickets

	p.ticketsMutex.Unlock()
	p.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientAuthList, builder))
}

func (p *Player) generateAppTicketFileCacheName(appId uint32) string {
	return fmt.Sprintf("cache/ticket_%d_%d.bin", p.GetSteamId().ToUint64(), appId)
}

func (p *Player) NormalizeTickets() {
	if len(p.tickets) > 499 {
		p.tickets = p.tickets[1:]
	}
}

func (p *Player) HeartBeat() {
	p.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientHeartBeat, new(protobuf.CMsgClientHeartBeat)))
}

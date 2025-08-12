package server

import (
	"google.golang.org/protobuf/proto"
	"net"
	"sync"
	"wirstaff.com/mirrors/protocol"
	"wirstaff.com/mirrors/protocol/protobuf"
	"wirstaff.com/mirrors/protocol/steamlang"
	"wirstaff.com/mirrors/steam"
	"wirstaff.com/mirrors/steamid"
)

type Client struct {
	ServerSocket      *net.UDPConn // подключение к игровому сервера
	ClientMessageSend chan []byte

	Server *Server
}

type Server struct {
	hostname   string
	mapName    string
	maxPlayers uint8
	secure     bool
	region     string
	port       uint32
	bots       uint8
	tags       string

	appid   uint32
	version string

	client *steam.Client

	tickets      []*protobuf.CMsgAuthTicket
	ticketsMutex sync.Mutex
}

func New() *Server {
	server := &Server{
		appid:   240,
		version: "9.99.9.9",
	}

	return server
}

func (s *Server) Connect() {
	s.client = steam.New()
	s.client.RegisterPacketHandler(s)
	s.client.Connect()
}

func (s *Server) Logon() {
	builder := new(protobuf.CMsgClientLogon)
	builder.ProtocolVersion = proto.Uint32(steamlang.MsgClientLogon_CurrentProtocol)
	builder.GameServerAppId = proto.Int32(int32(s.appid))
	s.client.SetSessionId(0)
	s.client.SetSteamId(uint64(steamid.NewIdAdv(0, 0, int32(steamlang.EUniverse_Public), int32(steamlang.EAccountType_GameServer))))
	s.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientLogonGameServer, builder))
}

func (s *Server) Events() <-chan interface{} {
	return s.client.Events()
}

func (s *Server) sendServerType() {
	builder := new(protobuf.CMsgGSServerType)
	builder.AppIdServed = proto.Uint32(s.appid)

	flags := steamlang.EServerFlags_Active | steamlang.EServerFlags_Dedicated | steamlang.EServerFlags_Linux

	if s.secure {
		flags = flags | steamlang.EServerFlags_Secure
	}

	builder.Flags = proto.Uint32(uint32(flags))
	builder.GameQueryPort = proto.Uint32(s.port)
	builder.GamePort = proto.Uint32(s.port)
	builder.GameVersion = proto.String(s.version)

	builder.GameDir = proto.String("cstrike")

	s.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_GSServerType, builder))
}

func (s *Server) sendServerData() {
	builder := new(protobuf.CMsgGameServerData)

	builder.Name = proto.String(s.hostname)
	builder.Map = proto.String(s.mapName)
	builder.QueryPort = proto.Uint32(s.port)
	builder.GamePort = proto.Uint32(s.port)
	builder.AppId = proto.Uint32(s.appid)
	builder.MaxPlayers = proto.Uint32(uint32(s.maxPlayers))
	builder.BotCount = proto.Uint32(uint32(s.bots))
	builder.Version = proto.String(s.version)
	builder.Secure = proto.Bool(s.secure)
	builder.Dedicated = proto.Bool(true)
	builder.GameType = proto.String(s.tags)

	builder.Product = proto.String("css")

	builder.Region = proto.String(s.region)
	builder.Gamedir = proto.String("cstrike")
	builder.Os = proto.String("l")

	s.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_AMGameServerUpdate, builder))
}

func (s *Server) AddFakeClient(steamId uint64, ticket []byte, crc uint32) {
	builder := new(protobuf.CMsgAuthTicket)

	builder.Gameid = proto.Uint64(uint64(s.appid))
	builder.Steamid = proto.Uint64(steamId)

	builder.Ticket = ticket

	builder.TicketCrc = proto.Uint32(crc)

	builder.Estate = proto.Uint32(1)
	builder.Eresult = proto.Uint32(0)

	s.ticketsMutex.Lock()
	s.tickets = append(s.tickets, builder)
	s.ticketsMutex.Unlock()
}

func (s *Server) SendTickets() {
	builder := new(protobuf.CMsgClientAuthList)
	builder.TokensLeft = proto.Uint32(0)
	builder.LastRequestSeq = proto.Uint32(0)
	builder.LastRequestSeqFromServer = proto.Uint32(0)
	s.ticketsMutex.Lock()
	builder.Tickets = s.tickets
	s.ticketsMutex.Unlock()
	builder.MessageSequence = proto.Uint32(uint32(len(s.tickets)))
	builder.AppIds = append(builder.AppIds, s.appid)
	s.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientAuthList, builder))
}

func (s *Server) HeartBeat() {
	s.client.Write(protocol.NewClientMsgProtobuf(steamlang.EMsg_ClientHeartBeat, new(protobuf.CMsgClientHeartBeat)))
}

func (s *Server) SetHostname(value string) {
	s.hostname = value
}

func (s *Server) SetMap(value string) {
	s.mapName = value
}

func (s *Server) SetMaxPlayers(value uint8) {
	s.maxPlayers = value
}

func (s *Server) SetSecure(value bool) {
	s.secure = value
}

func (s *Server) SetRegion(value string) {
	s.region = value
}

func (s *Server) SetPort(value uint32) {
	s.port = value
}

func (s *Server) SetBots(value uint8) {
	s.bots = value
}

func (s *Server) SetTags(value string) {
	s.tags = value
}

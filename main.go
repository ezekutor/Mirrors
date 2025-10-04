package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"wirstaff.com/mirrors/player"
	"wirstaff.com/mirrors/server"
	"wirstaff.com/mirrors/steam"
	"wirstaff.com/mirrors/utils"
)

type Config struct {
	StartPort uint32    `json:"start_port"`
	Servers   []Servers `json:"servers"`
}

type Servers struct {
	Count         uint32 `json:"count"`
	ServerAddress string `json:"server_address"`
	Players       uint8  `json:"players"`
	MaxPlayers    uint8  `json:"max_players"`
	Bots          uint8  `json:"bots"`
	Hostname      string `json:"hostname"`
	Map           string `json:"map"`
	Region        string `json:"region"`
	Secure        bool   `json:"secure"`
	Tags          string `json:"tags"`
	Description   string `json:"description"`
}

const (
	defaultConfigPath  = "config.json"
	defaultAccounts    = "accounts.txt"
	defaultTokens      = "tokens.txt"
	defaultVersionFile = "version.txt"
)

var (
	mirrorsMutex sync.RWMutex
	mirrors      []*server.Server

	playersMutex sync.RWMutex
	players      []*player.Player

var playersForServerMutex sync.RWMutex
var playersForServer = make(map[*server.Server][]*player.Player)

var playerAccountsMutex sync.Mutex
var playerAccounts = make(map[*player.Player]string)

var accounts []string
var tokens []string

var accountsMutex sync.Mutex
var accountsQueue []string

func main() {
	log.Println("Product ID: mirrors-x-cs2go")
	log.Println("Product Version: 0.1.0-beta")

	if err := os.MkdirAll("cache", 0o777); err != nil {
		log.Fatalf("Не удалось создать директорию cache: %v", err)
	}

	if err := steam.InitServers(); err != nil {
		log.Fatalf("Не удалось инициализировать список серверов Steam: %v", err)
	}

	configPath := flag.String("config", defaultConfigPath, "Путь до файла с конфигом")
	accountsPath := flag.String("accounts", defaultAccounts, "Путь до файла с аккаунтами")
	tokensPath := flag.String("tokens", defaultTokens, "Путь до файла с токенами")
	versionPath := flag.String("version", defaultVersionFile, "Путь до файла с версией игры")
	flag.Parse()

	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Не удалось загрузить конфиг: %v", err)
	}

	accounts = utils.ReadFile(*accountsPath)
	tokens = utils.ReadFile(*tokensPath)

	version, err := loadVersion(*versionPath)
	if err != nil {
		log.Fatalf("Не удалось загрузить версию игры: %v", err)
	}
	setGameVersion(version)

	go autoUpdate(*versionPath)
	go heartbeatLoop()
	go startMirrors(config)

	background()
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("файл %s не найден", path)
	}

	cfg := new(Config)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("неправильная структура конфига: %w", err)
	}

	return cfg, nil
}

func loadVersion(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		version := strings.TrimSpace(string(data))
		if version != "" {
			log.Printf("[INFO] Cached version %s", version)
			return version, nil
		}
	}

	log.Println("[INFO] Fetching game version…")
	version, err := fetchSteamVersion()
	if err != nil {
		return "", err
	}

	if err := saveVersion(path, version); err != nil {
		return "", err
	}

	accountsMutex.Lock()
	accountsQueue = append(accountsQueue, accounts...)
	accountsMutex.Unlock()

	go heartbeatLoop()

	return version, nil
}

	quit := background()
	sig := <-quit
	log.Printf("Получен сигнал завершения: %s", sig)
	signal.Stop(quit)
}

func fetchSteamVersion() (string, error) {
	resp, err := httpClient.Get("https://api.steampowered.com/ISteamApps/UpToDateCheck/v1/?appid=730&version=0")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("steam api returned status %d", resp.StatusCode)
	}

	tokensCount := 0
	var tokensMutex sync.Mutex

	for _, item := range config.Servers {
		item := item
		for range item.Count {
			tokensMutex.Lock()
			if tokensCount >= len(tokens)-1 {
				tokensMutex.Unlock()
				continue
			}
			token := tokens[tokensCount]
			tokensCount++
			tokensMutex.Unlock()

			portMutex.Lock()
			port := startPort
			startPort++
			portMutex.Unlock()

			go runMirror(item, token, port)
		}
	}
}

func runMirror(item Servers, token string, port uint32) {
	s := server.New()
	s.SetHostname(item.Hostname)
	s.SetMap(item.Map)
	s.SetMaxPlayers(item.MaxPlayers)
	s.SetPort(port)
	s.SetSecure(item.Secure)
	s.SetRegion(item.Region)
	s.SetBots(item.Bots)
	s.SetTags(item.Tags)
	s.Connect()

	started := false

	for event := range s.Events() {
		switch e := event.(type) {
		case *steam.ConnectedEvent:
			s.Logon(token)
			continue
		case *server.LoggedOnEvent:
			if e.Result == 1 {
				log.Printf("Зеркало %s запущено на порту %d\n", item.Hostname, port)
				mirrorsMutex.Lock()
				mirrors = append(mirrors, s)
				mirrorsMutex.Unlock()
				started = true
			}
		case steam.FatalErrorEvent:
		case error:
		}

		break
	}

	if !started {
		return
	}

	playersList := startPlayersForServer(s, item)

	playersForServerMutex.Lock()
	playersForServer[s] = append([]*player.Player(nil), playersList...)
	playersForServerMutex.Unlock()

	fmt.Println("Send Tickets")
	s.SendTickets()

	ticker := time.NewTicker(6 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		playersForServerMutex.Lock()
		currentPlayers := playersForServer[s]
		delete(playersForServer, s)
		playersForServerMutex.Unlock()

		for _, p := range currentPlayers {
			cleanupPlayer(p)
		}

		newPlayers := startPlayersForServer(s, item)

		playersForServerMutex.Lock()
		playersForServer[s] = append([]*player.Player(nil), newPlayers...)
		playersForServerMutex.Unlock()

		s.SendTickets()
	}
}

func startPlayersForServer(s *server.Server, item Servers) []*player.Player {
	startedPlayers := make([]*player.Player, 0, item.Players)

	for i := uint8(0); i < item.Players; i++ {
		account, ok := acquireAccount()
		if !ok {
			break
		}

		credentials := strings.SplitN(account, ":", 2)
		if len(credentials) != 2 {
			releaseAccount(account)
			continue
		}

		p := player.New()
		loggedOn := false

		for event := range p.Events() {
			switch e := event.(type) {
			case *steam.ConnectedEvent:
				p.Logon(credentials[0], credentials[1])
				continue
			case *player.LoggedOnEvent:
				if e.Result == 1 {
					log.Printf("Аккаунт %s авторизован\n", credentials[0])
					playersMutex.Lock()
					players = append(players, p)
					playersMutex.Unlock()

					playerAccountsMutex.Lock()
					playerAccounts[p] = account
					playerAccountsMutex.Unlock()

					startedPlayers = append(startedPlayers, p)
					loggedOn = true

					p.GetAppOwnershipTicket(730)
					continue
				}
			case *player.AppOwnershipTicketResponse:
				ticket := e.Ticket
				if ticket != nil {
					if result, err := p.AuthSessionTicket(e.Ticket); err == nil {
						s.AddFakeClient(result.SteamId, result.Ticket, result.Crc)
					}
				}
			case steam.FatalErrorEvent:
			case error:
			}

			break
		}

		if !loggedOn {
			p.Logoff()
			releaseAccount(account)
			continue
		}

		if item.Bots > 0 {
			break
		}
	}

	return startedPlayers
}

func acquireAccount() (string, bool) {
	accountsMutex.Lock()
	defer accountsMutex.Unlock()

	if len(accountsQueue) == 0 {
		return "", false
	}

	account := accountsQueue[0]
	accountsQueue = accountsQueue[1:]

	return account, true
}

func releaseAccount(account string) {
	if account == "" {
		return
	}

	accountsMutex.Lock()
	accountsQueue = append(accountsQueue, account)
	accountsMutex.Unlock()
}

func cleanupPlayer(p *player.Player) {
	if p == nil {
		return
	}

	p.Logoff()
	releaseAccountForPlayer(p)
	removePlayer(p)
}

func releaseAccountForPlayer(p *player.Player) {
	playerAccountsMutex.Lock()
	account, ok := playerAccounts[p]
	if ok {
		delete(playerAccounts, p)
	}
	playerAccountsMutex.Unlock()

	if ok {
		releaseAccount(account)
	}
}

func removePlayer(target *player.Player) {
	playersMutex.Lock()
	defer playersMutex.Unlock()

	for i, p := range players {
		if p == target {
			players = append(players[:i], players[i+1:]...)
			break
		}
	}
}

func runMirror(tmpl Servers, token string, port uint32, accountsMutex *sync.Mutex, accountsIdx *int) {
	srv := server.New()
	srv.SetHostname(tmpl.Hostname)
	srv.SetMap(tmpl.Map)
	srv.SetMaxPlayers(tmpl.MaxPlayers)
	srv.SetPort(port)
	srv.SetSecure(tmpl.Secure)
	srv.SetRegion(tmpl.Region)
	srv.SetBots(tmpl.Bots)
	srv.SetTags(tmpl.Tags)
	srv.SetVersion(getGameVersion())

	srv.Connect()

	for event := range srv.Events() {
		switch e := event.(type) {
		case *steam.ConnectedEvent:
			srv.Logon(token)
		case *server.LoggedOnEvent:
			if e.Result == 1 {
				log.Printf("Зеркало %s запущено на порту %d", tmpl.Hostname, port)
				mirrorsMutex.Lock()
				mirrors = append(mirrors, srv)
				mirrorsMutex.Unlock()
				startPlayersForServer(srv, tmpl, accountsMutex, accountsIdx)
				srv.SendTickets()
			} else {
				log.Printf("Не удалось авторизовать зеркало %s. Код: %d", tmpl.Hostname, e.Result)
			}
		case steam.FatalErrorEvent:
			log.Printf("[Mirror] %s завершено с ошибкой: %v", tmpl.Hostname, e)
			return
		case error:
			log.Printf("[Mirror] %s ошибка: %v", tmpl.Hostname, e)
		}
	}
}

func startPlayersForServer(srv *server.Server, tmpl Servers, accountsMutex *sync.Mutex, accountsIdx *int) {
	for i := uint8(0); i < tmpl.Players; i++ {
		accountsMutex.Lock()
		if *accountsIdx >= len(accounts) {
			accountsMutex.Unlock()
			log.Println("[WARN] accounts.txt exhausted")
			return
		}

		accountLine := accounts[*accountsIdx]
		(*accountsIdx)++
		accountsMutex.Unlock()

		credentials := strings.SplitN(accountLine, ":", 2)
		if len(credentials) != 2 {
			log.Printf("[WARN] некорректные учетные данные: %s", accountLine)
			continue
		}

		login := strings.TrimSpace(credentials[0])
		password := strings.TrimSpace(credentials[1])
		if login == "" || password == "" {
			log.Printf("[WARN] пустой логин или пароль: %s", accountLine)
			continue
		}

		startPlayer(srv, login, password)

		if tmpl.Bots > 0 {
			break
		}
	}
}

func startPlayer(srv *server.Server, login, password string) {
	p := player.New()

	go func() {
		for event := range p.Events() {
			switch e := event.(type) {
			case *steam.ConnectedEvent:
				p.Logon(login, password)
			case *player.LoggedOnEvent:
				if e.Result == 1 {
					log.Printf("Аккаунт %s авторизован", login)
					playersMutex.Lock()
					players = append(players, p)
					playersMutex.Unlock()
					p.GetAppOwnershipTicket(730)
				} else {
					log.Printf("Не удалось авторизовать аккаунт %s. Код: %d", login, e.Result)
					return
				}
			case *player.AppOwnershipTicketResponse:
				ticket := e.Ticket
				if ticket != nil {
					result, err := p.AuthSessionTicket(ticket)
					if err == nil {
						srv.AddFakeClient(result.SteamId, result.Ticket, result.Crc)
						srv.SendTickets()
					} else {
						log.Printf("[Player] ошибка AuthSessionTicket %s: %v", login, err)
					}
				}
			case steam.FatalErrorEvent:
				log.Printf("[Player] %s завершился с ошибкой: %v", login, e)
				return
			case error:
				log.Printf("[Player] %s ошибка: %v", login, e)
			}
		}
	}()
}

func heartbeatLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		mirrorsMutex.RLock()
		for _, m := range mirrors {
			m.HeartBeat()
		}
		mirrorsMutex.RUnlock()

		playersMutex.RLock()
		for _, p := range players {
			p.HeartBeat()
		}
		playersMutex.RUnlock()
	}
}

func startUDPServer(port uint32, tmpl Servers) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: int(port)}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return err
	}

	log.Printf("[UDP] Bound %d", port)

	go func() {
		defer conn.Close()
		buf := make([]byte, 2048)
		for {
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Temporary() {
					continue
				}
				log.Printf("[UDP] error on port %d: %v", port, err)
				return
			}

			if n < 5 {
				continue
			}

			response := handleUDPRequest(buf[:n], tmpl, port)
			if len(response) == 0 {
				continue
			}

			if _, err := conn.WriteToUDP(response, raddr); err != nil {
				log.Printf("[UDP] send error on port %d: %v", port, err)
			}
		}
	}()

	return nil
}

func handleUDPRequest(msg []byte, tmpl Servers, port uint32) []byte {
	command := msg[4]
	switch command {
	case 0x54:
		return buildInfoResponse(tmpl, port)
	case 0x55:
		return buildEmptyPlayerResponse()
	case 0x71:
		return buildRedirectResponse(tmpl.ServerAddress)
	case 0x56:
		return buildChallengeResponse()
	default:
		return nil
	}
}

func buildInfoResponse(tmpl Servers, port uint32) []byte {
	header := make([]byte, 6)
	header[0] = 0xFF
	header[1] = 0xFF
	header[2] = 0xFF
	header[3] = 0xFF
	header[4] = 0x49
	header[5] = 0x11

	parts := [][]byte{
		zeroTerminated(tmpl.Hostname),
		zeroTerminated(tmpl.Map),
		zeroTerminated("csgo"),
		zeroTerminated(tmpl.Description),
	}

	tail := []byte{
		0xDA, 0x02,
		0x00,
		tmpl.MaxPlayers,
		tmpl.Bots,
		'd',
		'l',
		0x00,
		boolToByte(tmpl.Secure),
	}

	version := zeroTerminated(getGameVersion())
	portBytes := []byte{byte(port & 0xFF), byte((port >> 8) & 0xFF)}
	rest := []byte{0xA1}
	rest = append(rest, portBytes...)
	rest = append(rest, 0x00)

	response := append(header, bytesJoin(parts)...)
	response = append(response, tail...)
	response = append(response, version...)
	response = append(response, rest...)

	return response
}

func buildEmptyPlayerResponse() []byte {
	return []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x44, 0x00}
}

func buildRedirectResponse(address string) []byte {
	response := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x39}
	response = append(response, []byte("ConnectRedirectAddress:")...)
	response = append(response, []byte(address)...)
	response = append(response, 0x00)
	return response
}

func buildChallengeResponse() []byte {
	response := make([]byte, 9)
	response[0] = 0xFF
	response[1] = 0xFF
	response[2] = 0xFF
	response[3] = 0xFF
	response[4] = 0x41
	rand := time.Now().UnixNano() & 0x7FFFFFFF
	response[5] = byte(rand)
	response[6] = byte(rand >> 8)
	response[7] = byte(rand >> 16)
	response[8] = byte(rand >> 24)
	return response
}

func zeroTerminated(value string) []byte {
	return append([]byte(value), 0x00)
}

func bytesJoin(parts [][]byte) []byte {
	size := 0
	for _, p := range parts {
		size += len(p)
	}

	result := make([]byte, 0, size)
	for _, p := range parts {
		result = append(result, p...)
	}
	return result
}

func boolToByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func background() chan os.Signal {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	return quit
}

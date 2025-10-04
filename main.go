package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
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
	CSGOMod   bool      `json:"csgo_mod"`
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
	UseAbuse      bool   `json:"use_abuse"`
}

var mirrorsMutex sync.RWMutex
var mirrors []*server.Server

var playersMutex sync.RWMutex
var players []*player.Player

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

	var err error

	os.Mkdir("cache", 0777)

	steam.InitServers(err)

	var configPath *string
	var accountsPath *string
	var tokensPath *string

	configPath = flag.String("config", "config.json", "Путь до файла с конфигом")
	accountsPath = flag.String("accounts", "accounts.txt", "Путь до файла с аккаунтами")
	tokensPath = flag.String("tokens", "tokens.txt", "Путь до файла с токенами")

	flag.Parse()

	var config = new(Config)

	content, err := ioutil.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Файл %s не найден\n", *configPath)
	}

	err = json.Unmarshal(content, &config)
	if err != nil {
		log.Fatalf("Неправильная структура конфига. %s\n", err)
	}

	accounts = utils.ReadFile(*accountsPath)
	tokens = utils.ReadFile(*tokensPath)

	accountsMutex.Lock()
	accountsQueue = append(accountsQueue, accounts...)
	accountsMutex.Unlock()

	go heartbeatLoop()

	go loadServers(config)

	quit := background()
	sig := <-quit
	log.Printf("Получен сигнал завершения: %s", sig)
	signal.Stop(quit)
}

func loadServers(config *Config) {
	var startPort = config.StartPort
	var portMutex sync.Mutex

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

			go runMirror(item, token, port, config.CSGOMod)
		}
	}
}

func runMirror(item Servers, token string, port uint32, csgoMod bool) {
	s := server.New()
	s.SetHostname(item.Hostname)
	s.SetMap(item.Map)
	s.SetMaxPlayers(item.MaxPlayers)
	s.SetPort(port)
	s.SetSecure(item.Secure)
	s.SetRegion(item.Region)
	s.SetBots(item.Bots)
	s.SetCSGOMod(csgoMod)
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

		if item.Bots > 0 || item.UseAbuse {
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

func heartbeatLoop() {
	heartbeat := time.NewTicker(10 * time.Second)
	for {
		<-heartbeat.C

		go func() {
			mirrorsMutex.RLock()
			for _, m := range mirrors {
				go m.HeartBeat()
			}
			mirrorsMutex.RUnlock()
		}()

		go func() {
			playersMutex.RLock()
			for _, p := range players {
				go p.HeartBeat()
			}
			playersMutex.RUnlock()
		}()
	}
}

func background() chan os.Signal {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	return quit
}

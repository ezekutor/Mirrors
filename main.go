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

var accounts []string
var tokens []string

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
	accountsCount := 0
	var tokensMutex sync.Mutex
	var accountsMutex sync.Mutex

	for _, item := range config.Servers {
		for range item.Count {
			if tokensCount < len(tokens)-1 {
				tokensMutex.Lock()
				token := tokens[tokensCount]
				tokensCount++
				tokensMutex.Unlock()

				go func() {
					s := server.New()
					s.SetHostname(item.Hostname)
					s.SetMap(item.Map)
					s.SetMaxPlayers(item.MaxPlayers)
					portMutex.Lock()
					s.SetPort(startPort)
					port := startPort
					startPort++
					portMutex.Unlock()
					s.SetSecure(item.Secure)
					s.SetRegion(item.Region)
					s.SetBots(item.Bots)
					s.SetCSGOMod(config.CSGOMod)
					s.SetTags(item.Tags)
					s.Connect()

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
							}
							break
						case steam.FatalErrorEvent:
							break
						case error:
							break
						}

						break
					}

					for range item.Players {
						accountsMutex.Lock()
						if accountsCount < len(accounts)-1 && item.Players > 0 {
							p := player.New()

							credentials := strings.Split(accounts[accountsCount], ":")

							for event := range p.Events() {
								switch e := event.(type) {
								case *steam.ConnectedEvent:
									p.Logon(credentials[0], credentials[1])
									accountsCount++
									continue
								case *player.LoggedOnEvent:
									if e.Result == 1 {
										log.Printf("Аккаунт %s авторизован\n", credentials[0])
										playersMutex.Lock()
										players = append(players, p)
										playersMutex.Unlock()

										p.GetAppOwnershipTicket(730)

										continue
									}

									break
								case *player.AppOwnershipTicketResponse:
									ticket := e.Ticket
									if ticket != nil {
										result, err := p.AuthSessionTicket(e.Ticket)

										if err == nil {
											s.AddFakeClient(result.SteamId, result.Ticket, result.Crc)
										}
									}
									break
								case steam.FatalErrorEvent:
									break
								case error:
									break
								}

								break
							}

							if item.Bots > 0 || item.UseAbuse {
								break
							}
						}

						accountsMutex.Unlock()
					}

					fmt.Println("Send Tickets")
					s.SendTickets()
				}()
			}
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
